plugins {
    id("com.android.application") version "9.2.1"
}

// The companion's sources and resources: the standalone service extends its
// accessibility service, serves the same protocol on a socket of its own and
// adds commands that act on the phone.
val companion = "../companion/app/src/main"

android {
    // The companion's namespace, so that its sources find BuildConfig and R.
    namespace = "app.pinky.companion"
    compileSdk {
        version = release(36) {
            minorApiLevel = 1
        }
    }

    defaultConfig {
        applicationId = "app.pinky.standalone"
        minSdk = 29
        targetSdk = 36
        versionCode = 1
        versionName = "0.1-prototype"
        // The companion protocol's socket, and the core's socket for
        // Pinky's own screens.
        buildConfigField("String", "SOCKET_NAME", "\"pinky_standalone\"")
        buildConfigField("String", "CORE_SOCKET", "\"pinky_standalone_core\"")
        ndk {
            abiFilters += "arm64-v8a"
        }
    }

    sourceSets {
        getByName("main") {
            kotlin.srcDir("$companion/kotlin")
            res.srcDirs("src/main/res", "$companion/res")
            // libpinkycore.so, which scripts/standalone.sh builds.
            jniLibs.srcDirs("build/go/jniLibs")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures {
        buildConfig = true
    }
}
