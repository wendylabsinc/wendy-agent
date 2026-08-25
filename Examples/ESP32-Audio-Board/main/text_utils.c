#include "text_utils.h"

#include <ctype.h>
#include <stdbool.h>
#include <stdlib.h>
#include <string.h>

// Matches a markdown link "[text](url)" at the start of `s`.
// require_nonempty mirrors the two different Python regexes this is based
// on: the citation-group scanner requires `[^\]]+`/`[^)\s]+` (non-empty
// text and URL), while the leftover standalone-link pass is the more
// lenient `[^\]]*`/`[^)\s]*` (either may be empty).
static bool match_markdown_link(const char *s, bool require_nonempty,
                                 size_t *out_consumed, size_t *out_text_start,
                                 size_t *out_text_len) {
    if (s[0] != '[') {
        return false;
    }
    size_t i = 1;
    while (s[i] != '\0' && s[i] != ']') {
        i++;
    }
    if (s[i] != ']') {
        return false;
    }
    size_t text_len = i - 1;
    if (require_nonempty && text_len == 0) {
        return false;
    }
    i++; // past ']'
    if (s[i] != '(') {
        return false;
    }
    i++; // past '('
    size_t url_start = i;
    while (s[i] != '\0' && s[i] != ')' && !isspace((unsigned char)s[i])) {
        i++;
    }
    if (s[i] != ')') {
        return false;
    }
    size_t url_len = i - url_start;
    if (require_nonempty && url_len == 0) {
        return false;
    }
    i++; // past ')'

    *out_consumed = i;
    *out_text_start = 1;
    *out_text_len = text_len;
    return true;
}

// Matches "\s*\((?:\[...\]\(...\)(?:,\s*)?)+\)" at the start of `s`: a
// parenthesized run of one-or-more citation links, each optionally followed
// by a comma and whitespace, with nothing else inside the parens.
static bool match_citation_group(const char *s, size_t *out_consumed) {
    size_t i = 0;
    while (isspace((unsigned char)s[i])) {
        i++;
    }
    if (s[i] != '(') {
        return false;
    }
    i++;

    int link_count = 0;
    for (;;) {
        size_t consumed, text_start, text_len;
        if (!match_markdown_link(s + i, /*require_nonempty=*/true, &consumed,
                                  &text_start, &text_len)) {
            break;
        }
        i += consumed;
        link_count++;
        if (s[i] == ',') {
            size_t j = i + 1;
            while (isspace((unsigned char)s[j])) {
                j++;
            }
            i = j;
        }
    }
    if (link_count == 0 || s[i] != ')') {
        return false;
    }
    i++;
    *out_consumed = i;
    return true;
}

// Both passes only ever remove or shorten text, so an output buffer the
// same size as the input is always a safe upper bound — no growth needed.
static char *strip_citation_groups(const char *input) {
    size_t len = strlen(input);
    char *out = malloc(len + 1);
    if (!out) {
        return NULL;
    }
    size_t oi = 0;
    size_t i = 0;
    while (input[i] != '\0') {
        size_t consumed;
        if (match_citation_group(input + i, &consumed)) {
            i += consumed;
            continue;
        }
        out[oi++] = input[i++];
    }
    out[oi] = '\0';
    return out;
}

static char *replace_standalone_links(const char *input) {
    size_t len = strlen(input);
    char *out = malloc(len + 1);
    if (!out) {
        return NULL;
    }
    size_t oi = 0;
    size_t i = 0;
    while (input[i] != '\0') {
        size_t consumed, text_start, text_len;
        if (match_markdown_link(input + i, /*require_nonempty=*/false,
                                 &consumed, &text_start, &text_len)) {
            memcpy(out + oi, input + i + text_start, text_len);
            oi += text_len;
            i += consumed;
            continue;
        }
        out[oi++] = input[i++];
    }
    out[oi] = '\0';
    return out;
}

char *strip_citations(const char *input) {
    if (!input) {
        return NULL;
    }
    char *stage1 = strip_citation_groups(input);
    if (!stage1) {
        return NULL;
    }
    char *stage2 = replace_standalone_links(stage1);
    free(stage1);
    return stage2;
}
