#include "../internal/bridge.h"
typedef struct {
	int near_lossless, interleave, transform;
	int preset[5];
	int near_components[16];
} cj_ls_options;
int cj_ls_decode(const unsigned char *, size_t, int, uint64_t, cj_ls_options *, cj_result *);
int cj_ls_encode(const uint16_t *, cj_ls_options *, cj_result *);
