# Kotlin/JVM HelloWorld

Requires Go, a C compiler, Python 3.9+, and JDK 21. Set `JAVA_HOME` to your JDK installation. This is a JVM console app, not an Android app.

From the repository root:

```sh
python3 scripts/build.py native
python3 scripts/build.py jni
distributions/kotlin/gradlew -p distributions/kotlin jar --no-daemon
distributions/kotlin/gradlew -p examples/kotlin run --no-daemon
```

On Windows, use `python` and the wrapper `distributions/kotlin/gradlew.bat` instead.

Expected application output: `WendyHelloWorld() = 42`.

The example is a separate Gradle application. Its dependency is the local `distributions/kotlin/build/libs/wendy-core-<VERSION>-dev.jar`, which contains the JNI bridge and Go shared library. It does not use a project dependency, Maven Local, or a published Core artifact. The existing Gradle wrapper is reused to avoid duplicating it.
