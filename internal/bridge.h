#ifndef COALAURA_BRIDGE_H
#define COALAURA_BRIDGE_H
#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

typedef struct {
	uint32_t width, height, components, bits;
	uint16_t *samples;
	unsigned char *data;
	size_t size;
	char error[256];
} cj_result;

static inline int cj_fail(cj_result *result, const char *message) {
	snprintf(result->error, sizeof(result->error), "%s", message);
	return 0;
}

static inline int cj_check(cj_result *result, uint64_t max_pixels) {
	uint64_t count = (uint64_t)result->width * result->height;
	if (!max_pixels) max_pixels = 16u << 20;
	if (!count || !result->components || result->components > 16 || count > max_pixels || count > (512u << 20) / result->components / 2)
		return cj_fail(result, "resource limit exceeded");
	return 1;
}

static inline void cj_free(cj_result *result) {
	free(result->samples);
	free(result->data);
}
#endif
