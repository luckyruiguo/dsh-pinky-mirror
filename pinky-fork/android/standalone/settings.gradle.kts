// The standalone prototype: Pinky's core inside an Android app, with no
// computer at run time (docs/notes/standalone-phone.md). Not shipped. Built
// with the companion's Gradle wrapper after the Go core (make standalone):
// ../companion/gradlew -p . assembleDebug
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

rootProject.name = "pinky-standalone"
