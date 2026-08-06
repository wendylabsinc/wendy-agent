#pragma once

#ifdef __cplusplus
extern "C" {
#endif

// Strips OpenAI web-search citation markup so answers read cleanly when
// spoken: whole "(...[site](url)...)" citation groups are dropped, and any
// leftover standalone "[text](url)" is replaced with just "text". A hand-
// rolled approximation of VoiceAssistant's regex-based strip_citations() in
// app.py — this embedded build has no regex engine.
//
// Returns a newly malloc'd, NUL-terminated string the caller must free.
// Returns NULL only if `input` is NULL or allocation fails.
char *strip_citations(const char *input);

#ifdef __cplusplus
}
#endif
