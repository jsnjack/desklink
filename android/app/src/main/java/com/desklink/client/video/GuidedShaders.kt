package com.desklink.client.video

/**
 * The GLSL ES 3.00 programs of [GuidedRenderer]. The stream is 4:2:0: one
 * chroma sample per 2×2 pixels, so coloured edges blur when chroma is simply
 * interpolated. Within a small window, chroma tends to follow luma along a
 * line (dark blue text on grey: the darker, the bluer), so the renderer fits
 * chroma ≈ slope × luma + intercept around every chroma sample, from the
 * luma averaged over each 2×2 block, and applies the fit to the luma of each
 * pixel (a guided filter, with luma as the guide). Three passes:
 *
 * 1. [STATS], one fragment per chroma sample: the 2×2 block's mean Y' and
 *    the sample's Cb, Cr, into a half-size RGBA16F texture.
 * 2. [COEFFICIENTS], one fragment per chroma sample: means over the
 *    (2 × [RADIUS] + 1)² window around it, then the slope and intercept for
 *    Cb and Cr, into a second half-size RGBA16F texture.
 * 3. [COMPOSE], one fragment per stream pixel: the coefficients interpolated
 *    bilinearly at the pixel, applied to its own Y', converted to R'G'B'.
 *
 * Every sample is in normalised units (Y' 0..1, Cb and Cr -0.5..0.5,
 * [StreamColor.range]); [EPSILON] keeps the slope small where luma is flat.
 * The JVM test GuidedChromaTest runs the same arithmetic on a decoded frame.
 *
 * The decoder's frame is an external texture of the GL_EXT_YUV_target kind
 * (__samplerExternal2DY2YEXT), which returns Y, Cb and Cr as stored instead
 * of RGB. It is sampled with GL_NEAREST at stream pixel centres, so each
 * fetch returns one stored luma sample and the chroma sample of its 2×2
 * block, whatever filtering the driver would apply to the chroma plane.
 */
object GuidedShaders {
    /** The window radius in chroma samples: 5×5 chroma, 10×10 stream pixels. */
    const val RADIUS = 2

    /** The guided filter's regularisation: a luma variance of (2/255)². */
    const val EPSILON = (2.0 / 255) * (2.0 / 255)

    /** A triangle that covers the viewport, made from gl_VertexID alone. */
    const val FULL_SCREEN_VERTEX = """#version 300 es
void main() {
    vec2 p = vec2(float((gl_VertexID & 1) << 2) - 1.0, float((gl_VertexID & 2) << 1) - 1.0);
    gl_Position = vec4(p, 0.0, 1.0);
}
"""

    /**
     * The decoder's frame, sampled at a stream pixel (bottom-up, centres at
     * .5) through the SurfaceTexture mapping uMap (offset xy, scale zw). The
     * sampler is highp because samplers default to lowp, which need not hold
     * a 10-bit value.
     */
    const val YUV_INPUT = """#version 300 es
#extension GL_EXT_YUV_target : require
precision highp float;
precision highp int;
uniform highp __samplerExternal2DY2YEXT uYuv;
uniform vec4 uMap;
uniform vec2 uVideo;
vec3 yuvAt(vec2 pixel) {
    return texture(uYuv, uMap.xy + uMap.zw * (pixel / uVideo)).rgb;
}
"""

    /** Pass 1, after [YUV_INPUT]: mean luma of the 2×2 block and its chroma. */
    const val STATS = """
uniform vec4 uRange;
out vec4 outColor;
void main() {
    vec2 block = floor(gl_FragCoord.xy) * 2.0;
    // The block's top-left pixel holds its chroma sample in every siting.
    vec3 topLeft = yuvAt(block + vec2(0.5, 1.5));
    float luma = topLeft.r + yuvAt(block + vec2(1.5, 1.5)).r
        + yuvAt(block + vec2(0.5, 0.5)).r + yuvAt(block + vec2(1.5, 0.5)).r;
    outColor = vec4((luma * 0.25 - uRange.x) * uRange.y, (topLeft.gb - uRange.z) * uRange.w, 0.0);
}
"""

    /** Pass 2: the slope and intercept of chroma against luma around each chroma sample. */
    const val COEFFICIENTS = """#version 300 es
precision highp float;
precision highp int;
#define RADIUS $RADIUS
uniform highp sampler2D uStats;
uniform float uEpsilon;
out vec4 outColor;
void main() {
    ivec2 last = textureSize(uStats, 0) - 1;
    ivec2 centre = ivec2(gl_FragCoord.xy);
    vec3 sum = vec3(0.0);
    vec3 products = vec3(0.0);
    for (int dy = -RADIUS; dy <= RADIUS; dy++) {
        for (int dx = -RADIUS; dx <= RADIUS; dx++) {
            vec3 v = texelFetch(uStats, clamp(centre + ivec2(dx, dy), ivec2(0), last), 0).rgb;
            sum += v;
            products += v.r * v;
        }
    }
    float n = float((2 * RADIUS + 1) * (2 * RADIUS + 1));
    vec3 mean = sum / n;
    vec3 meanProducts = products / n;
    float variance = max(meanProducts.r - mean.r * mean.r, 0.0);
    vec2 slope = (meanProducts.gb - mean.r * mean.gb) / (variance + uEpsilon);
    vec2 intercept = mean.gb - slope * mean.r;
    outColor = vec4(slope.x, intercept.x, slope.y, intercept.y);
}
"""

    /** Pass 3, after [YUV_INPUT]: each pixel's chroma from its own luma, then R'G'B'. */
    const val COMPOSE = """
uniform highp sampler2D uCoefficients;
uniform vec4 uRange;
uniform mat3 uToRgb;
uniform vec2 uTargetScale;
out vec4 outColor;
void main() {
    vec2 pixel = gl_FragCoord.xy * uTargetScale;
    float luma = (yuvAt(pixel).r - uRange.x) * uRange.y;
    vec4 k = texture(uCoefficients, pixel / uVideo);
    vec2 chroma = k.xz * luma + k.yw;
    outColor = vec4(clamp(uToRgb * vec3(luma, chroma), 0.0, 1.0), 1.0);
}
"""

    /** An overlay quad: uRect is left, top, width, height in stream pixels from the top-left corner. */
    const val QUAD_VERTEX = """#version 300 es
uniform vec4 uRect;
uniform vec2 uVideo;
out vec2 vTexel;
void main() {
    vec2 corner = vec2(float(gl_VertexID & 1), float((gl_VertexID >> 1) & 1));
    vec2 pixel = uRect.xy + corner * uRect.zw;
    vTexel = corner;
    gl_Position = vec4(pixel.x / uVideo.x * 2.0 - 1.0, 1.0 - pixel.y / uVideo.y * 2.0, 0.0, 1.0);
}
"""

    /** An overlay quad's texture, premultiplied. */
    const val QUAD_FRAGMENT = """#version 300 es
precision mediump float;
uniform sampler2D uTile;
in vec2 vTexel;
out vec4 outColor;
void main() {
    outColor = texture(uTile, vTexel);
}
"""
}
