// Host-buildable tests for the pure-C helpers that have no ESP-IDF
// dependency. Run via ./run_tests.sh — no target hardware or toolchain
// needed. Everything else in this project (audio, WiFi, the Realtime
// session, barge-in) is hardware-in-the-loop and isn't covered here.

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "../main/text_utils.h"
#include "../main/weather_codes.h"

static int failures = 0;

#define EXPECT_STR_EQ(actual, expected)                                      \
    do {                                                                     \
        const char *_a = (actual);                                          \
        const char *_e = (expected);                                        \
        if (strcmp(_a, _e) != 0) {                                          \
            printf("FAIL %s:%d: expected \"%s\", got \"%s\"\n", __FILE__,    \
                   __LINE__, _e, _a);                                       \
            failures++;                                                     \
        } else {                                                            \
            printf("PASS %s:%d\n", __FILE__, __LINE__);                     \
        }                                                                    \
    } while (0)

static void test_weather_codes(void) {
    EXPECT_STR_EQ(describe_weather_code(0, true), "clear sky");
    EXPECT_STR_EQ(describe_weather_code(63, true), "moderate rain");
    EXPECT_STR_EQ(describe_weather_code(95, true), "thunderstorm");
    EXPECT_STR_EQ(describe_weather_code(999, true), "unknown conditions");
    // A missing code must not be confused with the (also valid) code 0.
    EXPECT_STR_EQ(describe_weather_code(0, false), "unknown conditions");
}

static void test_strip_citations(void) {
    char *r;

    r = strip_citations("The sky is blue.");
    EXPECT_STR_EQ(r, "The sky is blue.");
    free(r);

    // A single-citation group is dropped entirely, including its leading
    // whitespace -- there is deliberately no space left before the period.
    r = strip_citations("It rained today ([weather.com](https://weather.com)).");
    EXPECT_STR_EQ(r, "It rained today.");
    free(r);

    // Multiple links inside one parenthetical, comma-separated.
    r = strip_citations(
        "Score was 3-1 ([espn](https://espn.com), [bbc](https://bbc.com)).");
    EXPECT_STR_EQ(r, "Score was 3-1.");
    free(r);

    // A citation group in the middle of a sentence: only the ONE leading
    // space before the parens is consumed, so the surrounding text still
    // reads naturally with a single space.
    r = strip_citations(
        "Sunshine tomorrow ([source](https://x.com)) according to reports.");
    EXPECT_STR_EQ(r, "Sunshine tomorrow according to reports.");
    free(r);

    // A standalone markdown link (not wrapped in citation parens) keeps
    // its surrounding text and is replaced with just its inner text.
    r = strip_citations("See [this article](https://example.com) for more.");
    EXPECT_STR_EQ(r, "See this article for more.");
    free(r);

    r = strip_citations("");
    EXPECT_STR_EQ(r, "");
    free(r);

    if (strip_citations(NULL) != NULL) {
        printf("FAIL: strip_citations(NULL) should return NULL\n");
        failures++;
    } else {
        printf("PASS: strip_citations(NULL) returns NULL\n");
    }
}

int main(void) {
    test_weather_codes();
    test_strip_citations();

    if (failures == 0) {
        printf("\nAll tests passed.\n");
        return 0;
    }
    printf("\n%d test(s) failed.\n", failures);
    return 1;
}
