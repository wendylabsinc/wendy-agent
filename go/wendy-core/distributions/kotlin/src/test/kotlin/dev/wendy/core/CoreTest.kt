package dev.wendy.core

import kotlin.test.Test
import kotlin.test.assertEquals

class CoreTest {
    @Test fun reachesTheSharedEngine() {
        repeat(100) { assertEquals(42, WendyHelloWorld()) }
    }
}
