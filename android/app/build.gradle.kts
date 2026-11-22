plugins {
    id("com.android.application")
    kotlin("plugin.serialization")
    kotlin("plugin.compose")
}

android {
    namespace = "com.desklink.client"
    compileSdk = 37
    buildToolsVersion = "37.0.0"

    defaultConfig {
        applicationId = "com.desklink.client"
        // Android 12: MediaCodec KEY_LOW_LATENCY, pointer capture of touchpads
        // and SurfaceView frame-rate hints are all available. Built for the
        // Galaxy Tab S9.
        minSdk = 31
        targetSdk = 35
        // Stamped by the Makefile from monova (-PappVersion=x.y.z); the code
        // is derived so every release installs over the previous one.
        val appVersion = (project.findProperty("appVersion") as String?)
            ?.takeIf { it.isNotBlank() && it != "0.0.0" } ?: "0.0.1"
        val (major, minor, patch) = appVersion.split('.').map { it.takeWhile(Char::isDigit).toIntOrNull() ?: 0 }
        versionCode = maxOf(1, major * 1_000_000 + minor * 1_000 + patch)
        versionName = appVersion
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
        debug {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures { compose = true }

    lint {
        // targetSdk 35 is the project's specified target; compileSdk follows the shared toolchain.
        disable += "OldTargetApi"
    }
}

dependencies {
    implementation(platform("androidx.compose:compose-bom:2026.09.00"))
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.core:core-ktx:1.19.1")
    implementation("androidx.activity:activity-compose:1.13.0")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.11.0")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.11.0")
    // TLS with a pinned certificate fingerprint and the control WebSocket.
    implementation("com.squareup.okhttp3:okhttp:5.5.0")
    // Scans the pairing QR code through Play services, without a camera permission.
    implementation("com.google.android.gms:play-services-code-scanner:16.1.0")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.11.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.11.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-play-services:1.11.0")
    testImplementation("junit:junit:4.13.2")
}
