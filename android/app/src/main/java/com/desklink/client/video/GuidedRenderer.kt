package com.desklink.client.video

import android.graphics.SurfaceTexture
import android.opengl.EGL14
import android.opengl.EGLConfig
import android.opengl.EGLContext
import android.opengl.EGLDisplay
import android.opengl.EGLExt
import android.opengl.EGLSurface
import android.opengl.GLES11Ext
import android.opengl.GLES30
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.os.Process
import android.util.Log
import android.view.Surface
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

/** Told the host's capture time of each frame the renderer sent to the screen. */
fun interface FrameShown {
    /** The frame the host captured at [captureMicros] (its clock) was just queued for display. */
    fun shown(captureMicros: Long)
}

/**
 * Draws the decoder's frames onto the SurfaceView's [output] with chroma
 * rebuilt at every pixel from luma ([GuidedShaders]), so coloured text keeps
 * sharp edges. The decoder renders into [input], a SurfaceTexture's surface
 * read as an external YUV texture; each frame is drawn 1:1 at the stream's
 * [width] × [height] (the SurfaceView's buffer has that fixed size and the
 * compositor scales it, as it does the decoder's own buffers) as soon as it
 * arrives, and queued without waiting for the display, so a newer frame
 * replaces one not yet shown.
 *
 * Each frame's GPU time (GL_EXT_disjoint_timer_query, else the CPU time to
 * submit it) goes to a [RenderBudget]; [onSecond] gets each second's summary,
 * and [onGiveUp] is called once, with the reason, when drawing failed or
 * stayed too slow, after which the renderer draws nothing more.
 */
class GuidedRenderer(
    private val output: Surface,
    private val width: Int,
    private val height: Int,
    private val onShown: FrameShown,
    private val onSecond: (second: RenderSecond, gpuTimed: Boolean) -> Unit,
    private val onGiveUp: (reason: String) -> Unit,
) {
    private val thread = HandlerThread("desklink-render", Process.THREAD_PRIORITY_URGENT_DISPLAY).apply { start() }
    private val handler = Handler(thread.looper)

    private var display: EGLDisplay = EGL14.EGL_NO_DISPLAY
    private var context: EGLContext = EGL14.EGL_NO_CONTEXT
    private var surface: EGLSurface = EGL14.EGL_NO_SURFACE
    private var surfaceTexture: SurfaceTexture? = null
    private var inputSurface: Surface? = null

    private var statsProgram = 0
    private var coefficientsProgram = 0
    private var composeProgram = 0
    private var statsMap = -1
    private var composeMap = -1
    private var composeTargetScale = -1
    private var yuvTexture = 0
    private var statsTexture = 0
    private var coefficientsTexture = 0
    private var statsFramebuffer = 0
    private var coefficientsFramebuffer = 0
    private lateinit var quads: OverlayQuads

    private val matrix = FloatArray(16)
    private val map = FloatArray(4)
    private val targetSize = IntArray(2)
    private val scratch = IntArray(1)
    private val disjoint = IntArray(1)
    private val colourAttachment = intArrayOf(GLES30.GL_COLOR_ATTACHMENT0)
    private val windowColour = intArrayOf(GLES30.GL_COLOR)

    private var timed = false
    private val queries = IntArray(QUERIES)
    private val queryPending = BooleanArray(QUERIES)
    private var querySlot = 0
    private val budget = RenderBudget()

    private val pending = AtomicInteger()
    private val redrawPending = AtomicBoolean()
    private var drawnOnce = false
    private var gaveUp = false

    // The presentation time of the frame drawn last, in microseconds.
    private var frameMicros = 0L

    @Volatile
    private var released = false

    /**
     * Drawn above the video at stream pixels, from the next frame on; see
     * [VideoOverlay]. The renderer calls its release when its context goes.
     */
    @Volatile
    var overlay: VideoOverlay? = null

    /** The surface the decoder renders into; valid once [start] returned. */
    val input: Surface get() = checkNotNull(inputSurface) { "renderer not started" }

    private val frameAvailable = SurfaceTexture.OnFrameAvailableListener {
        if (pending.getAndIncrement() == 0) handler.post(drawFrame)
    }

    private val drawFrame = Runnable {
        val frames = pending.getAndSet(0)
        if (frames == 0 || gaveUp || released) return@Runnable
        try {
            val st = surfaceTexture ?: return@Runnable
            // Frames that queued up while the last one was drawn are skipped:
            // only the newest is worth showing.
            repeat(frames) { st.updateTexImage() }
            draw(newFrame = true)
        } catch (e: RuntimeException) {
            giveUp("drawing failed: ${e.message}")
        }
    }

    private val redrawFrame = Runnable {
        redrawPending.set(false)
        if (!drawnOnce || gaveUp || released) return@Runnable
        try {
            draw(newFrame = false)
        } catch (e: RuntimeException) {
            giveUp("drawing failed: ${e.message}")
        }
    }

    /**
     * Sets up EGL, the programs and the SurfaceTexture on the render thread
     * and waits for that; throws IllegalStateException with the reason when
     * this device cannot run the renderer, after releasing what it made.
     */
    fun start() {
        var error: RuntimeException? = null
        val done = CountDownLatch(1)
        handler.post {
            try {
                setUp()
            } catch (e: RuntimeException) {
                error = e
                tearDown()
            } finally {
                done.countDown()
            }
        }
        if (!done.await(START_WAIT_MS, TimeUnit.MILLISECONDS)) {
            release()
            throw IllegalStateException("the GPU renderer did not start within $START_WAIT_MS ms")
        }
        error?.let {
            thread.quitSafely()
            throw IllegalStateException(it.message ?: it.toString(), it)
        }
    }

    /** Draws the last frame again, for an overlay that changed between frames. */
    fun redraw() {
        if (!redrawPending.getAndSet(true)) handler.post(redrawFrame)
    }

    /**
     * Stops drawing and releases the GL context, the SurfaceTexture and the
     * EGL surface on the render thread, waiting up to [RELEASE_WAIT_MS], so
     * that the decoder or a new renderer can take the SurfaceView's surface.
     */
    fun release() {
        released = true
        val done = CountDownLatch(1)
        val posted = handler.post {
            tearDown()
            done.countDown()
            thread.quitSafely()
        }
        if (!posted || Looper.myLooper() == thread.looper) return
        if (!done.await(RELEASE_WAIT_MS, TimeUnit.MILLISECONDS)) Log.w(TAG, "renderer release still running")
    }

    private fun setUp() {
        check(width > 0 && height > 0 && width % 2 == 0 && height % 2 == 0) { "the stream size ${width}x$height is not even" }
        val d = EGL14.eglGetDisplay(EGL14.EGL_DEFAULT_DISPLAY)
        check(d != EGL14.EGL_NO_DISPLAY) { "no EGL display" }
        val version = IntArray(2)
        check(EGL14.eglInitialize(d, version, 0, version, 1)) { "eglInitialize: ${eglError()}" }
        display = d
        val configs = arrayOfNulls<EGLConfig>(1)
        val count = IntArray(1)
        val configAttribs = intArrayOf(
            EGL14.EGL_RED_SIZE, 8, EGL14.EGL_GREEN_SIZE, 8, EGL14.EGL_BLUE_SIZE, 8, EGL14.EGL_ALPHA_SIZE, 8,
            EGL14.EGL_RENDERABLE_TYPE, EGLExt.EGL_OPENGL_ES3_BIT_KHR, EGL14.EGL_SURFACE_TYPE, EGL14.EGL_WINDOW_BIT,
            EGL14.EGL_NONE,
        )
        check(EGL14.eglChooseConfig(d, configAttribs, 0, configs, 0, 1, count, 0) && count[0] > 0) { "no OpenGL ES 3 RGBA8888 EGL config" }
        val config = checkNotNull(configs[0])
        val eglExtensions = EGL14.eglQueryString(d, EGL14.EGL_EXTENSIONS).orEmpty()
        // A high-priority context lets frames overtake other apps' GPU work;
        // drivers that do not grant it fall back to the normal priority.
        val contextAttribs = if (hasExtension(eglExtensions, "EGL_IMG_context_priority")) {
            intArrayOf(EGL14.EGL_CONTEXT_CLIENT_VERSION, 3, EGL_CONTEXT_PRIORITY_LEVEL_IMG, EGL_CONTEXT_PRIORITY_HIGH_IMG, EGL14.EGL_NONE)
        } else {
            intArrayOf(EGL14.EGL_CONTEXT_CLIENT_VERSION, 3, EGL14.EGL_NONE)
        }
        context = EGL14.eglCreateContext(d, config, EGL14.EGL_NO_CONTEXT, contextAttribs, 0)
        check(context != EGL14.EGL_NO_CONTEXT) { "eglCreateContext: ${eglError()}" }
        surface = EGL14.eglCreateWindowSurface(d, config, output, intArrayOf(EGL14.EGL_NONE), 0)
        check(surface != EGL14.EGL_NO_SURFACE) { "eglCreateWindowSurface: ${eglError()}" }
        check(EGL14.eglMakeCurrent(d, surface, surface, context)) { "eglMakeCurrent: ${eglError()}" }
        // Queue each frame without waiting for the display: a newer frame
        // replaces one not yet shown instead of waiting behind it.
        EGL14.eglSwapInterval(d, 0)

        val extensions = GLES30.glGetString(GLES30.GL_EXTENSIONS).orEmpty()
        check(hasExtension(extensions, "GL_EXT_YUV_target")) { "the GPU lacks GL_EXT_YUV_target, which reads the decoder's planes" }
        timed = hasExtension(extensions, "GL_EXT_disjoint_timer_query")
        if (timed) GLES30.glGenQueries(QUERIES, queries, 0)

        statsProgram = program(GuidedShaders.FULL_SCREEN_VERTEX, GuidedShaders.YUV_INPUT + GuidedShaders.STATS, "stats")
        coefficientsProgram = program(GuidedShaders.FULL_SCREEN_VERTEX, GuidedShaders.COEFFICIENTS, "coefficients")
        composeProgram = program(GuidedShaders.FULL_SCREEN_VERTEX, GuidedShaders.YUV_INPUT + GuidedShaders.COMPOSE, "compose")
        val quadProgram = program(GuidedShaders.QUAD_VERTEX, GuidedShaders.QUAD_FRAGMENT, "overlay quad")
        statsMap = setUpYuvInput(statsProgram)
        composeMap = setUpYuvInput(composeProgram)
        GLES30.glUseProgram(coefficientsProgram)
        GLES30.glUniform1i(GLES30.glGetUniformLocation(coefficientsProgram, "uStats"), STATS_UNIT)
        GLES30.glUniform1f(GLES30.glGetUniformLocation(coefficientsProgram, "uEpsilon"), GuidedShaders.EPSILON.toFloat())
        GLES30.glUseProgram(composeProgram)
        GLES30.glUniform1i(GLES30.glGetUniformLocation(composeProgram, "uCoefficients"), COEFFICIENTS_UNIT)
        GLES30.glUniformMatrix3fv(GLES30.glGetUniformLocation(composeProgram, "uToRgb"), 1, false, StreamColor.toRgb, 0)
        composeTargetScale = GLES30.glGetUniformLocation(composeProgram, "uTargetScale")
        GLES30.glUseProgram(quadProgram)
        GLES30.glUniform1i(GLES30.glGetUniformLocation(quadProgram, "uTile"), OverlayQuads.UNIT)
        GLES30.glUniform1i(GLES30.glGetUniformLocation(quadProgram, "uMask"), OverlayQuads.MASK_UNIT)
        GLES30.glUniform2f(GLES30.glGetUniformLocation(quadProgram, "uVideo"), width.toFloat(), height.toFloat())
        val samplers = IntArray(2)
        GLES30.glGenSamplers(2, samplers, 0)
        sampler(samplers[0], GLES30.GL_NEAREST)
        sampler(samplers[1], GLES30.GL_LINEAR)
        quads = OverlayQuads(
            quadProgram,
            GLES30.glGetUniformLocation(quadProgram, "uRect"),
            GLES30.glGetUniformLocation(quadProgram, "uMaskSpan"),
            samplers[0],
            samplers[1],
        )

        val textures = IntArray(3)
        GLES30.glGenTextures(3, textures, 0)
        yuvTexture = textures[0]
        statsTexture = textures[1]
        coefficientsTexture = textures[2]
        GLES30.glBindTexture(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, yuvTexture)
        filter(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, GLES30.GL_NEAREST)
        val framebuffers = IntArray(2)
        GLES30.glGenFramebuffers(2, framebuffers, 0)
        statsFramebuffer = framebuffers[0]
        coefficientsFramebuffer = framebuffers[1]
        // The statistics are read texel by texel; the coefficients are
        // interpolated between chroma samples for each pixel.
        halfSizeTarget(statsTexture, statsFramebuffer, GLES30.GL_NEAREST, "statistics")
        halfSizeTarget(coefficientsTexture, coefficientsFramebuffer, GLES30.GL_LINEAR, "coefficients")
        GLES30.glBindFramebuffer(GLES30.GL_FRAMEBUFFER, 0)
        // Dithering would move the output off the exact rounding of each value.
        GLES30.glDisable(GLES30.GL_DITHER)
        checkGl("setting up")

        val st = SurfaceTexture(yuvTexture)
        st.setDefaultBufferSize(width, height)
        st.setOnFrameAvailableListener(frameAvailable, handler)
        surfaceTexture = st
        inputSurface = Surface(st)
        Log.i(TAG, "GPU renderer ready at ${width}x$height, ${if (timed) "GPU" else "CPU"} timing")
    }

    /** Sets the uniforms of [YUV_INPUT][GuidedShaders.YUV_INPUT] that never change; returns uMap's location. */
    private fun setUpYuvInput(program: Int): Int {
        GLES30.glUseProgram(program)
        GLES30.glUniform1i(GLES30.glGetUniformLocation(program, "uYuv"), YUV_UNIT)
        GLES30.glUniform2f(GLES30.glGetUniformLocation(program, "uVideo"), width.toFloat(), height.toFloat())
        GLES30.glUniform4fv(GLES30.glGetUniformLocation(program, "uRange"), 1, StreamColor.range, 0)
        return GLES30.glGetUniformLocation(program, "uMap")
    }

    private fun halfSizeTarget(texture: Int, framebuffer: Int, filter: Int, name: String) {
        GLES30.glBindTexture(GLES30.GL_TEXTURE_2D, texture)
        GLES30.glTexStorage2D(GLES30.GL_TEXTURE_2D, 1, GLES30.GL_RGBA16F, width / 2, height / 2)
        filter(GLES30.GL_TEXTURE_2D, filter)
        GLES30.glBindFramebuffer(GLES30.GL_FRAMEBUFFER, framebuffer)
        GLES30.glFramebufferTexture2D(GLES30.GL_FRAMEBUFFER, GLES30.GL_COLOR_ATTACHMENT0, GLES30.GL_TEXTURE_2D, texture, 0)
        val status = GLES30.glCheckFramebufferStatus(GLES30.GL_FRAMEBUFFER)
        check(status == GLES30.GL_FRAMEBUFFER_COMPLETE) { "the $name target (RGBA16F) is not renderable: 0x${status.toString(16)}" }
    }

    private fun sampler(sampler: Int, filter: Int) {
        GLES30.glSamplerParameteri(sampler, GLES30.GL_TEXTURE_MIN_FILTER, filter)
        GLES30.glSamplerParameteri(sampler, GLES30.GL_TEXTURE_MAG_FILTER, filter)
        GLES30.glSamplerParameteri(sampler, GLES30.GL_TEXTURE_WRAP_S, GLES30.GL_CLAMP_TO_EDGE)
        GLES30.glSamplerParameteri(sampler, GLES30.GL_TEXTURE_WRAP_T, GLES30.GL_CLAMP_TO_EDGE)
    }

    private fun filter(target: Int, filter: Int) {
        GLES30.glTexParameteri(target, GLES30.GL_TEXTURE_MIN_FILTER, filter)
        GLES30.glTexParameteri(target, GLES30.GL_TEXTURE_MAG_FILTER, filter)
        GLES30.glTexParameteri(target, GLES30.GL_TEXTURE_WRAP_S, GLES30.GL_CLAMP_TO_EDGE)
        GLES30.glTexParameteri(target, GLES30.GL_TEXTURE_WRAP_T, GLES30.GL_CLAMP_TO_EDGE)
    }

    private fun draw(newFrame: Boolean) {
        val began = System.nanoTime()
        val timing = newFrame && timed && beginTimer()
        GLES30.glActiveTexture(GLES30.GL_TEXTURE0 + YUV_UNIT)
        GLES30.glBindTexture(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, yuvTexture)
        GLES30.glActiveTexture(GLES30.GL_TEXTURE0 + STATS_UNIT)
        GLES30.glBindTexture(GLES30.GL_TEXTURE_2D, statsTexture)
        GLES30.glActiveTexture(GLES30.GL_TEXTURE0 + COEFFICIENTS_UNIT)
        GLES30.glBindTexture(GLES30.GL_TEXTURE_2D, coefficientsTexture)
        if (newFrame) {
            val st = checkNotNull(surfaceTexture)
            // MediaCodec gives a rendered buffer its presentation time, the
            // host's capture time, in nanoseconds.
            frameMicros = st.timestamp / 1000
            st.getTransformMatrix(matrix)
            TextureMapping.unshrink(matrix, map)
            halfSizePass(statsFramebuffer, statsProgram, statsMap)
            halfSizePass(coefficientsFramebuffer, coefficientsProgram, -1)
        }
        GLES30.glBindFramebuffer(GLES30.GL_FRAMEBUFFER, 0)
        // The window's buffer has the stream's size once the SurfaceView's
        // fixed size applies; until then the frame is scaled to fit it.
        EGL14.eglQuerySurface(display, surface, EGL14.EGL_WIDTH, targetSize, 0)
        EGL14.eglQuerySurface(display, surface, EGL14.EGL_HEIGHT, targetSize, 1)
        GLES30.glViewport(0, 0, targetSize[0], targetSize[1])
        GLES30.glInvalidateFramebuffer(GLES30.GL_FRAMEBUFFER, 1, windowColour, 0)
        GLES30.glUseProgram(composeProgram)
        GLES30.glUniform4fv(composeMap, 1, map, 0)
        GLES30.glUniform2f(composeTargetScale, width.toFloat() / targetSize[0], height.toFloat() / targetSize[1])
        GLES30.glDrawArrays(GLES30.GL_TRIANGLES, 0, 3)
        overlay?.let { drawOverlay(it) }
        if (timing) GLES30.glEndQuery(GL_TIME_ELAPSED_EXT)
        if (!EGL14.eglSwapBuffers(display, surface)) {
            giveUp("eglSwapBuffers: ${eglError()}")
            return
        }
        if (!drawnOnce) {
            drawnOnce = true
            // A decoder format the YUV sampler cannot read fails at draw time.
            val error = GLES30.glGetError()
            if (error != GLES30.GL_NO_ERROR) {
                giveUp("the first frame failed with GL error 0x${error.toString(16)}")
                return
            }
        }
        if (!newFrame) return
        if (frameMicros > 0) onShown.shown(frameMicros)
        if (!timed) judge(System.nanoTime() - began)
    }

    private fun halfSizePass(framebuffer: Int, program: Int, mapLocation: Int) {
        GLES30.glBindFramebuffer(GLES30.GL_FRAMEBUFFER, framebuffer)
        GLES30.glViewport(0, 0, width / 2, height / 2)
        // Every texel is rewritten, so the old contents need not be loaded.
        GLES30.glInvalidateFramebuffer(GLES30.GL_FRAMEBUFFER, 1, colourAttachment, 0)
        GLES30.glUseProgram(program)
        if (mapLocation >= 0) GLES30.glUniform4fv(mapLocation, 1, map, 0)
        GLES30.glDrawArrays(GLES30.GL_TRIANGLES, 0, 3)
    }

    private fun drawOverlay(o: VideoOverlay) {
        GLES30.glEnable(GLES30.GL_BLEND)
        GLES30.glBlendFunc(GLES30.GL_ONE, GLES30.GL_ONE_MINUS_SRC_ALPHA)
        o.draw(quads, frameMicros)
        GLES30.glDisable(GLES30.GL_BLEND)
    }

    /**
     * Reads the timer query this slot started a few frames ago, if done, and
     * starts the next; returns false when the slot is still busy, so this
     * frame goes untimed.
     */
    private fun beginTimer(): Boolean {
        val slot = querySlot
        if (queryPending[slot]) {
            GLES30.glGetQueryObjectuiv(queries[slot], GLES30.GL_QUERY_RESULT_AVAILABLE, scratch, 0)
            if (scratch[0] == 0) return false
            GLES30.glGetQueryObjectuiv(queries[slot], GLES30.GL_QUERY_RESULT, scratch, 0)
            queryPending[slot] = false
            // A disjoint event (a GPU clock change) makes the result meaningless.
            GLES30.glGetIntegerv(GL_GPU_DISJOINT_EXT, disjoint, 0)
            if (disjoint[0] == 0) judge(scratch[0].toLong() and 0xffffffffL)
        }
        GLES30.glBeginQuery(GL_TIME_ELAPSED_EXT, queries[slot])
        queryPending[slot] = true
        querySlot = (slot + 1) % QUERIES
        return true
    }

    private fun judge(renderNanos: Long) {
        budget.record(renderNanos, System.nanoTime())?.let { second ->
            Log.d(TAG, "render: ${second.frames} frames, mean ${second.meanMicros} µs, max ${second.maxMicros} µs, ${second.slowFrames} slow")
            onSecond(second, timed)
        }
        if (budget.exhausted) {
            giveUp("drawing took over ${RenderBudget.DEFAULT_BUDGET_NANOS / 1000} µs per frame for several seconds")
        }
    }

    private fun giveUp(reason: String) {
        if (gaveUp) return
        gaveUp = true
        onGiveUp(reason)
    }

    private fun tearDown() {
        surfaceTexture?.setOnFrameAvailableListener(null)
        if (context != EGL14.EGL_NO_CONTEXT && surface != EGL14.EGL_NO_SURFACE) {
            runCatching { overlay?.release() }.onFailure { Log.w(TAG, "overlay release", it) }
        }
        inputSurface?.release()
        inputSurface = null
        surfaceTexture?.release()
        surfaceTexture = null
        if (display != EGL14.EGL_NO_DISPLAY) {
            // Destroying the context frees every GL object in it. The display
            // stays initialised: the app's UI renderer shares it.
            EGL14.eglMakeCurrent(display, EGL14.EGL_NO_SURFACE, EGL14.EGL_NO_SURFACE, EGL14.EGL_NO_CONTEXT)
            if (surface != EGL14.EGL_NO_SURFACE) EGL14.eglDestroySurface(display, surface)
            if (context != EGL14.EGL_NO_CONTEXT) EGL14.eglDestroyContext(display, context)
            EGL14.eglReleaseThread()
        }
        surface = EGL14.EGL_NO_SURFACE
        context = EGL14.EGL_NO_CONTEXT
        display = EGL14.EGL_NO_DISPLAY
    }

    private fun program(vertex: String, fragment: String, name: String): Int {
        val vs = shader(GLES30.GL_VERTEX_SHADER, vertex, name)
        val fs = shader(GLES30.GL_FRAGMENT_SHADER, fragment, name)
        val p = GLES30.glCreateProgram()
        GLES30.glAttachShader(p, vs)
        GLES30.glAttachShader(p, fs)
        GLES30.glLinkProgram(p)
        GLES30.glDeleteShader(vs)
        GLES30.glDeleteShader(fs)
        GLES30.glGetProgramiv(p, GLES30.GL_LINK_STATUS, scratch, 0)
        check(scratch[0] != 0) { "the $name program did not link: ${GLES30.glGetProgramInfoLog(p)}" }
        return p
    }

    private fun shader(type: Int, source: String, name: String): Int {
        val s = GLES30.glCreateShader(type)
        GLES30.glShaderSource(s, source)
        GLES30.glCompileShader(s)
        GLES30.glGetShaderiv(s, GLES30.GL_COMPILE_STATUS, scratch, 0)
        check(scratch[0] != 0) { "the $name shader did not compile: ${GLES30.glGetShaderInfoLog(s)}" }
        return s
    }

    private fun checkGl(step: String) {
        val error = GLES30.glGetError()
        check(error == GLES30.GL_NO_ERROR) { "GL error 0x${error.toString(16)} while $step" }
    }

    private fun eglError(): String = "0x" + EGL14.eglGetError().toString(16)

    private fun hasExtension(list: String, name: String): Boolean = list.split(' ').contains(name)

    private companion object {
        const val TAG = "DeskLink"
        const val START_WAIT_MS = 2000L
        const val RELEASE_WAIT_MS = 1000L
        const val QUERIES = 4
        const val YUV_UNIT = 0
        const val COEFFICIENTS_UNIT = 1
        const val STATS_UNIT = 2

        // GL_EXT_disjoint_timer_query, which GLES30 does not name.
        const val GL_TIME_ELAPSED_EXT = 0x88BF
        const val GL_GPU_DISJOINT_EXT = 0x8FBB

        // EGL_IMG_context_priority.
        const val EGL_CONTEXT_PRIORITY_LEVEL_IMG = 0x3100
        const val EGL_CONTEXT_PRIORITY_HIGH_IMG = 0x3101
    }
}
