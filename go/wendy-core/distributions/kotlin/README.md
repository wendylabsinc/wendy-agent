# Wendy Core for Kotlin/JVM

The Maven artifact `dev.wendy:wendy-core` provides thin JNI access to the same native Core release.

```kotlin
import dev.wendy.core.WendyHelloWorld
check(WendyHelloWorld() == 42)
```

The release jar contains every supported native target plus a minimal JNI loader. JDK 21+ and a Kotlin compiler compatible with Kotlin 2.4 metadata are required. Libraries are extracted to a private temporary directory and loaded for the process lifetime; runtime notices accompany them.

Kotlin/JVM distribution support is not an Android platform-support claim. Android/Native/WASM variants require their own verified target integration. This is not the future ergonomic Wendy Kotlin SDK.
