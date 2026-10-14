plugins {
    kotlin("jvm") version "2.4.21"
    `maven-publish`
}

group = "dev.wendy"
version = providers.gradleProperty("coreVersion").get()
repositories { mavenCentral() }
kotlin { jvmToolchain(21) }
dependencies { testImplementation(kotlin("test")) }
tasks.test { useJUnitPlatform() }
java { withSourcesJar(); withJavadocJar() }

publishing {
    publications {
        create<MavenPublication>("core") {
            from(components["java"])
            pom {
                name.set("Wendy Core")
                description.set("Thin Kotlin/JVM access to the shared Wendy Core engine")
                url.set("https://github.com/wendylabsinc/wendy-core")
            }
        }
    }
    repositories {
        maven {
            name = "release"
            url = uri(providers.environmentVariable("MAVEN_PUBLISH_URL").orElse("https://maven.pkg.github.com/wendylabsinc/wendy-core").get())
            credentials {
                username = providers.environmentVariable("MAVEN_USERNAME").orNull
                password = providers.environmentVariable("MAVEN_PASSWORD").orNull
            }
        }
    }
}
