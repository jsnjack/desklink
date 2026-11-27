// The pointer's position and shape from Mutter's screen cast.
//
// With cursor-mode metadata, Mutter leaves the pointer out of the frames and
// describes it in each PipeWire buffer's SPA_META_Cursor, sending a buffer
// without pixels when only the pointer changed. pipewiresrc drops those
// buffers ("Buffer is empty") and passes on no bitmap, so desklinkd reads the
// metadata where every buffer passes: pipewiresrc's pw_stream_dequeue_buffer
// call. desklinkd defines that function and exports it from its executable
// (the Makefile links it with --export-dynamic-symbol), so the pipewire
// plugin, loaded later, binds to it ahead of libpipewire's; it calls
// libpipewire's own, reads the metadata and returns the buffer unchanged.
//
// The structs are the stable PipeWire ABI (spa/buffer/buffer.h,
// spa/buffer/meta.h, pipewire/stream.h), declared here because the build
// does not need pipewire-devel.

#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include "_cgo_export.h"
#include "cursorhook.h"

struct spa_meta {
	uint32_t type;
	uint32_t size;
	void *data;
};
struct spa_chunk {
	uint32_t offset;
	uint32_t size;
	int32_t stride;
	int32_t flags;
};
struct spa_data {
	uint32_t type;
	uint32_t flags;
	int64_t fd;
	uint32_t mapoffset;
	uint32_t maxsize;
	void *data;
	struct spa_chunk *chunk;
};
struct spa_buffer {
	uint32_t n_metas;
	uint32_t n_datas;
	struct spa_meta *metas;
	struct spa_data *datas;
};
struct pw_buffer {
	struct spa_buffer *buffer;
};
struct spa_point {
	int32_t x, y;
};
struct spa_rectangle {
	uint32_t width, height;
};
struct spa_meta_bitmap {
	uint32_t format;
	struct spa_rectangle size;
	int32_t stride;
	uint32_t offset;
};
struct spa_meta_cursor {
	uint32_t id;
	uint32_t flags;
	struct spa_point position;
	struct spa_point hotspot;
	uint32_t bitmap_offset;
};

#define SPA_META_Cursor 5

// Bitmaps larger than pipewiresrc asks room for (384 x 384) are ignored.
#define MAX_BITMAP_SIDE 384

struct pw_stream;
typedef struct pw_buffer *(*dequeue_fn)(struct pw_stream *);

static _Atomic(dequeue_fn) real_dequeue;
static atomic_long hook_buffers;

// The last position reported, so an unchanged pointer is not reported again
// for every frame. Only the capture's streaming thread writes them; while a
// new pipeline replaces an old one both may, which at worst reports a
// position twice.
static int last_x = INT32_MIN, last_y = INT32_MIN;

void desklink_cursor_inspect(void *spa_buffer) {
	struct spa_buffer *sb = spa_buffer;
	for (uint32_t i = 0; i < sb->n_metas; i++) {
		struct spa_meta *m = &sb->metas[i];
		if (m->type != SPA_META_Cursor || m->size < sizeof(struct spa_meta_cursor) || !m->data)
			continue;
		struct spa_meta_cursor *c = m->data;
		// id 0: the buffer says nothing about the pointer.
		if (c->id == 0)
			return;
		const struct spa_meta_bitmap *bm = NULL;
		if (c->bitmap_offset >= sizeof(*c) &&
		    c->bitmap_offset + sizeof(struct spa_meta_bitmap) <= m->size) {
			bm = (const struct spa_meta_bitmap *)((const uint8_t *)c + c->bitmap_offset);
			uint64_t need = (uint64_t)bm->offset + (uint64_t)bm->stride * bm->size.height;
			if (bm->size.width == 0 || bm->size.height == 0 || bm->size.width > MAX_BITMAP_SIDE ||
			    bm->size.height > MAX_BITMAP_SIDE || bm->stride < (int32_t)(bm->size.width * 4) ||
			    c->bitmap_offset + need > m->size)
				bm = NULL;
		}
		// A hidden pointer, or one off a mirrored monitor, comes as a
		// transparent 1 x 1 bitmap, so the shape alone says what shows.
		if (!bm && c->position.x == last_x && c->position.y == last_y)
			return;
		last_x = c->position.x;
		last_y = c->position.y;
		if (bm) {
			desklinkCursor(c->position.x, c->position.y, (int)bm->size.width, (int)bm->size.height,
				       bm->stride, bm->format, c->hotspot.x, c->hotspot.y,
				       (void *)((const uint8_t *)bm + bm->offset));
		} else {
			desklinkCursor(c->position.x, c->position.y, 0, 0, 0, 0, 0, 0, NULL);
		}
		return;
	}
}

struct pw_buffer *pw_stream_dequeue_buffer(struct pw_stream *s) {
	dequeue_fn real = atomic_load(&real_dequeue);
	if (!real) {
		void *h = dlopen("libpipewire-0.3.so.0", RTLD_NOW | RTLD_NOLOAD);
		if (h)
			real = (dequeue_fn)dlsym(h, "pw_stream_dequeue_buffer");
		if (!real)
			return NULL;
		atomic_store(&real_dequeue, real);
	}
	struct pw_buffer *b = real(s);
	if (b && b->buffer) {
		atomic_fetch_add(&hook_buffers, 1);
		desklink_cursor_inspect(b->buffer);
	}
	return b;
}

int desklink_cursor_hook_exported(void) {
	return dlsym(RTLD_DEFAULT, "pw_stream_dequeue_buffer") == (void *)pw_stream_dequeue_buffer;
}

long desklink_cursor_hook_buffers(void) { return atomic_load(&hook_buffers); }

void desklink_cursor_forget(void) {
	last_x = INT32_MIN;
	last_y = INT32_MIN;
}

void *desklink_cursor_test_buffer(int x, int y, int w, int h, int hx, int hy, uint32_t format, int empty) {
	size_t bitmap = w > 0 ? sizeof(struct spa_meta_bitmap) + (size_t)w * h * 4 : 0;
	size_t meta = sizeof(struct spa_meta_cursor) + bitmap;
	struct spa_buffer *sb = calloc(1, sizeof(*sb) + sizeof(struct spa_meta) + sizeof(struct spa_data) +
					      sizeof(struct spa_chunk) + meta);
	struct spa_meta *m = (struct spa_meta *)(sb + 1);
	struct spa_data *d = (struct spa_data *)(m + 1);
	struct spa_chunk *ch = (struct spa_chunk *)(d + 1);
	struct spa_meta_cursor *c = (struct spa_meta_cursor *)(ch + 1);
	sb->n_metas = 1;
	sb->metas = m;
	sb->n_datas = 1;
	sb->datas = d;
	d->chunk = ch;
	ch->size = empty ? 0 : 4096;
	m->type = SPA_META_Cursor;
	m->size = meta;
	m->data = c;
	c->id = 1;
	c->position.x = x;
	c->position.y = y;
	c->hotspot.x = hx;
	c->hotspot.y = hy;
	if (w > 0) {
		c->bitmap_offset = sizeof(*c);
		struct spa_meta_bitmap *bm = (struct spa_meta_bitmap *)(c + 1);
		bm->format = format;
		bm->size.width = w;
		bm->size.height = h;
		bm->stride = w * 4;
		bm->offset = sizeof(*bm);
		uint8_t *px = (uint8_t *)(bm + 1);
		for (int i = 0; i < w * h * 4; i++)
			px[i] = (uint8_t)i;
	}
	return sb;
}
