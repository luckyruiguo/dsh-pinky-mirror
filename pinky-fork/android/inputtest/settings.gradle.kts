// A test app with text fields that take input in different ways, for
// checking how Pinky types (see README.md). Built with the companion's
// Gradle wrapper: ../companion/gradlew -p . assembleDebug
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "pinky-inputtest"
