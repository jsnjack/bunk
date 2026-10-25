// Package gl is a minimal OpenGL 3.3 core binding for the glgrid spike.
//
// It loads every entry point at runtime: dlopen("libEGL.so.1") and
// eglGetProcAddress, with dlsym on libOpenGL.so.0 as a fallback. Nothing links
// against -lGL or -lEGL and no GL/EGL headers are needed, so the build works
// without libglvnd-devel. Stock github.com/go-gl/gl needs `pkg-config gl` (or
// `egl` with -tags egl) plus GL/glx.h or EGL/egl.h even when the caller only
// uses InitWithProcAddrFunc, because procaddr.go and package.go are always
// compiled.
//
// Only the functions the spike calls are bound. Each Go function is one cgo
// call; there is no per-call allocation.
package gl

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>

typedef unsigned int GLenum;
typedef unsigned int GLuint;
typedef int GLint;
typedef int GLsizei;
typedef float GLfloat;
typedef unsigned char GLboolean;
typedef unsigned char GLubyte;
typedef unsigned int GLbitfield;
typedef char GLchar;
typedef ptrdiff_t GLsizeiptr;
typedef ptrdiff_t GLintptr;
typedef uint64_t GLuint64;
typedef int64_t GLint64;

#define GLFN(ret, name, params) \
	typedef ret (*PFN_##name) params; \
	static PFN_##name p_##name;

GLFN(const GLubyte*, glGetString, (GLenum))
GLFN(void, glGetIntegerv, (GLenum, GLint*))
GLFN(GLenum, glGetError, (void))
GLFN(void, glViewport, (GLint, GLint, GLsizei, GLsizei))
GLFN(void, glClearColor, (GLfloat, GLfloat, GLfloat, GLfloat))
GLFN(void, glClear, (GLbitfield))
GLFN(void, glEnable, (GLenum))
GLFN(void, glDisable, (GLenum))
GLFN(void, glBlendFunc, (GLenum, GLenum))
GLFN(void, glFlush, (void))
GLFN(void, glFinish, (void))
GLFN(void, glGenVertexArrays, (GLsizei, GLuint*))
GLFN(void, glBindVertexArray, (GLuint))
GLFN(void, glGenBuffers, (GLsizei, GLuint*))
GLFN(void, glBindBuffer, (GLenum, GLuint))
GLFN(void, glBufferData, (GLenum, GLsizeiptr, const void*, GLenum))
GLFN(void, glBufferSubData, (GLenum, GLintptr, GLsizeiptr, const void*))
GLFN(void, glVertexAttribPointer, (GLuint, GLint, GLenum, GLboolean, GLsizei, const void*))
GLFN(void, glVertexAttribIPointer, (GLuint, GLint, GLenum, GLsizei, const void*))
GLFN(void, glEnableVertexAttribArray, (GLuint))
GLFN(void, glVertexAttribDivisor, (GLuint, GLuint))
GLFN(GLuint, glCreateShader, (GLenum))
GLFN(void, glShaderSource, (GLuint, GLsizei, const GLchar* const*, const GLint*))
GLFN(void, glCompileShader, (GLuint))
GLFN(void, glGetShaderiv, (GLuint, GLenum, GLint*))
GLFN(void, glGetShaderInfoLog, (GLuint, GLsizei, GLsizei*, GLchar*))
GLFN(void, glDeleteShader, (GLuint))
GLFN(GLuint, glCreateProgram, (void))
GLFN(void, glAttachShader, (GLuint, GLuint))
GLFN(void, glLinkProgram, (GLuint))
GLFN(void, glGetProgramiv, (GLuint, GLenum, GLint*))
GLFN(void, glGetProgramInfoLog, (GLuint, GLsizei, GLsizei*, GLchar*))
GLFN(void, glUseProgram, (GLuint))
GLFN(GLint, glGetUniformLocation, (GLuint, const GLchar*))
GLFN(void, glUniform1i, (GLint, GLint))
GLFN(void, glUniform2i, (GLint, GLint, GLint))
GLFN(void, glUniform2f, (GLint, GLfloat, GLfloat))
GLFN(void, glGenTextures, (GLsizei, GLuint*))
GLFN(void, glBindTexture, (GLenum, GLuint))
GLFN(void, glActiveTexture, (GLenum))
GLFN(void, glTexImage2D, (GLenum, GLint, GLint, GLsizei, GLsizei, GLint, GLenum, GLenum, const void*))
GLFN(void, glTexParameteri, (GLenum, GLenum, GLint))
GLFN(void, glPixelStorei, (GLenum, GLint))
GLFN(void, glDrawArraysInstanced, (GLenum, GLint, GLsizei, GLsizei))
GLFN(void, glGenQueries, (GLsizei, GLuint*))
GLFN(void, glBeginQuery, (GLenum, GLuint))
GLFN(void, glEndQuery, (GLenum))
GLFN(void, glGetQueryObjectiv, (GLuint, GLenum, GLint*))
GLFN(void, glGetQueryObjectui64v, (GLuint, GLenum, GLuint64*))
GLFN(void, glReadPixels, (GLint, GLint, GLsizei, GLsizei, GLenum, GLenum, void*))
GLFN(void, glGenFramebuffers, (GLsizei, GLuint*))
GLFN(void, glBindFramebuffer, (GLenum, GLuint))
GLFN(void, glFramebufferTexture2D, (GLenum, GLenum, GLenum, GLuint, GLint))
GLFN(GLenum, glCheckFramebufferStatus, (GLenum))
GLFN(void, glDeleteFramebuffers, (GLsizei, const GLuint*))
GLFN(void, glDeleteTextures, (GLsizei, const GLuint*))
GLFN(void*, glFenceSync, (GLenum, GLbitfield))
GLFN(void, glDeleteSync, (void*))
GLFN(void, glUniform1f, (GLint, GLfloat))
GLFN(void, glUniform4f, (GLint, GLfloat, GLfloat, GLfloat, GLfloat))
GLFN(void, glTexSubImage2D, (GLenum, GLint, GLint, GLint, GLsizei, GLsizei, GLenum, GLenum, const void*))
GLFN(void, glDrawArrays, (GLenum, GLint, GLsizei))

typedef void* (*PFN_eglGetProcAddress)(const char*);
static PFN_eglGetProcAddress p_eglGetProcAddress;
static void* libegl;
static void* libopengl;

static void* glgrid_sym(const char* name) {
	void* p = NULL;
	if (p_eglGetProcAddress) p = p_eglGetProcAddress(name);
	if (!p && libopengl) p = dlsym(libopengl, name);
	return p;
}

// glgrid_load returns NULL on success or the name of the first missing symbol.
static const char* glgrid_load(void) {
	libegl = dlopen("libEGL.so.1", RTLD_LAZY | RTLD_LOCAL);
	if (libegl) p_eglGetProcAddress = (PFN_eglGetProcAddress)dlsym(libegl, "eglGetProcAddress");
	libopengl = dlopen("libOpenGL.so.0", RTLD_LAZY | RTLD_LOCAL);
	if (!p_eglGetProcAddress && !libopengl) return "libEGL.so.1/libOpenGL.so.0";
#define LOAD(name) p_##name = (PFN_##name)glgrid_sym(#name); if (!p_##name) return #name;
	LOAD(glGetString) LOAD(glGetIntegerv) LOAD(glGetError) LOAD(glViewport)
	LOAD(glClearColor) LOAD(glClear) LOAD(glEnable) LOAD(glDisable) LOAD(glBlendFunc)
	LOAD(glFlush) LOAD(glFinish)
	LOAD(glGenVertexArrays) LOAD(glBindVertexArray) LOAD(glGenBuffers) LOAD(glBindBuffer)
	LOAD(glBufferData) LOAD(glBufferSubData) LOAD(glVertexAttribPointer)
	LOAD(glVertexAttribIPointer) LOAD(glEnableVertexAttribArray) LOAD(glVertexAttribDivisor)
	LOAD(glCreateShader) LOAD(glShaderSource) LOAD(glCompileShader) LOAD(glGetShaderiv)
	LOAD(glGetShaderInfoLog) LOAD(glDeleteShader) LOAD(glCreateProgram) LOAD(glAttachShader)
	LOAD(glLinkProgram) LOAD(glGetProgramiv) LOAD(glGetProgramInfoLog) LOAD(glUseProgram)
	LOAD(glGetUniformLocation) LOAD(glUniform1i) LOAD(glUniform2i) LOAD(glUniform2f)
	LOAD(glGenTextures) LOAD(glBindTexture) LOAD(glActiveTexture) LOAD(glTexImage2D)
	LOAD(glTexParameteri) LOAD(glPixelStorei) LOAD(glDrawArraysInstanced)
	LOAD(glGenQueries) LOAD(glBeginQuery) LOAD(glEndQuery) LOAD(glGetQueryObjectiv)
	LOAD(glGetQueryObjectui64v) LOAD(glReadPixels)
	LOAD(glGenFramebuffers) LOAD(glBindFramebuffer) LOAD(glFramebufferTexture2D)
	LOAD(glCheckFramebufferStatus) LOAD(glDeleteFramebuffers) LOAD(glDeleteTextures)
	LOAD(glFenceSync) LOAD(glDeleteSync) LOAD(glUniform1f) LOAD(glDrawArrays)
	LOAD(glUniform4f) LOAD(glTexSubImage2D)
#undef LOAD
	return NULL;
}

static int glgrid_has_egl(void) { return p_eglGetProcAddress != NULL; }

static const char* w_glGetString(GLenum n) { return (const char*)p_glGetString(n); }
static void w_glGetIntegerv(GLenum n, GLint* v) { p_glGetIntegerv(n, v); }
static GLenum w_glGetError(void) { return p_glGetError(); }
static void w_glViewport(GLint x, GLint y, GLsizei w, GLsizei h) { p_glViewport(x, y, w, h); }
static void w_glClearColor(GLfloat r, GLfloat g, GLfloat b, GLfloat a) { p_glClearColor(r, g, b, a); }
static void w_glClear(GLbitfield m) { p_glClear(m); }
static void w_glEnable(GLenum c) { p_glEnable(c); }
static void w_glDisable(GLenum c) { p_glDisable(c); }
static void w_glBlendFunc(GLenum s, GLenum d) { p_glBlendFunc(s, d); }
static void w_glFlush(void) { p_glFlush(); }
static void w_glFinish(void) { p_glFinish(); }
static GLuint w_glGenVertexArray(void) { GLuint v = 0; p_glGenVertexArrays(1, &v); return v; }
static void w_glBindVertexArray(GLuint v) { p_glBindVertexArray(v); }
static GLuint w_glGenBuffer(void) { GLuint v = 0; p_glGenBuffers(1, &v); return v; }
static void w_glBindBuffer(GLenum t, GLuint b) { p_glBindBuffer(t, b); }
static void w_glBufferData(GLenum t, GLsizeiptr n, const void* d, GLenum u) { p_glBufferData(t, n, d, u); }
static void w_glBufferSubData(GLenum t, GLintptr o, GLsizeiptr n, const void* d) { p_glBufferSubData(t, o, n, d); }
static void w_glVertexAttribPointer(GLuint i, GLint n, GLenum t, GLboolean norm, GLsizei s, uintptr_t off) {
	p_glVertexAttribPointer(i, n, t, norm, s, (const void*)off);
}
static void w_glVertexAttribIPointer(GLuint i, GLint n, GLenum t, GLsizei s, uintptr_t off) {
	p_glVertexAttribIPointer(i, n, t, s, (const void*)off);
}
static void w_glEnableVertexAttribArray(GLuint i) { p_glEnableVertexAttribArray(i); }
static void w_glVertexAttribDivisor(GLuint i, GLuint d) { p_glVertexAttribDivisor(i, d); }
static GLuint w_glCreateShader(GLenum t) { return p_glCreateShader(t); }
static void w_glShaderSource(GLuint s, const char* src) { p_glShaderSource(s, 1, &src, NULL); }
static void w_glCompileShader(GLuint s) { p_glCompileShader(s); }
static GLint w_glGetShaderiv(GLuint s, GLenum p) { GLint v = 0; p_glGetShaderiv(s, p, &v); return v; }
static void w_glGetShaderInfoLog(GLuint s, GLsizei n, char* buf) { p_glGetShaderInfoLog(s, n, NULL, buf); }
static void w_glDeleteShader(GLuint s) { p_glDeleteShader(s); }
static GLuint w_glCreateProgram(void) { return p_glCreateProgram(); }
static void w_glAttachShader(GLuint p, GLuint s) { p_glAttachShader(p, s); }
static void w_glLinkProgram(GLuint p) { p_glLinkProgram(p); }
static GLint w_glGetProgramiv(GLuint p, GLenum n) { GLint v = 0; p_glGetProgramiv(p, n, &v); return v; }
static void w_glGetProgramInfoLog(GLuint p, GLsizei n, char* buf) { p_glGetProgramInfoLog(p, n, NULL, buf); }
static void w_glUseProgram(GLuint p) { p_glUseProgram(p); }
static GLint w_glGetUniformLocation(GLuint p, const char* n) { return p_glGetUniformLocation(p, n); }
static void w_glUniform1i(GLint l, GLint a) { p_glUniform1i(l, a); }
static void w_glUniform2i(GLint l, GLint a, GLint b) { p_glUniform2i(l, a, b); }
static void w_glUniform2f(GLint l, GLfloat a, GLfloat b) { p_glUniform2f(l, a, b); }
static GLuint w_glGenTexture(void) { GLuint v = 0; p_glGenTextures(1, &v); return v; }
static void w_glBindTexture(GLenum t, GLuint x) { p_glBindTexture(t, x); }
static void w_glActiveTexture(GLenum t) { p_glActiveTexture(t); }
static void w_glTexImage2D(GLenum t, GLint l, GLint ifmt, GLsizei w, GLsizei h, GLenum fmt, GLenum ty, const void* d) {
	p_glTexImage2D(t, l, ifmt, w, h, 0, fmt, ty, d);
}
static void w_glTexParameteri(GLenum t, GLenum p, GLint v) { p_glTexParameteri(t, p, v); }
static void w_glPixelStorei(GLenum p, GLint v) { p_glPixelStorei(p, v); }
static void w_glDrawArraysInstanced(GLenum m, GLint f, GLsizei c, GLsizei n) { p_glDrawArraysInstanced(m, f, c, n); }
static GLuint w_glGenQuery(void) { GLuint v = 0; p_glGenQueries(1, &v); return v; }
static void w_glBeginQuery(GLenum t, GLuint q) { p_glBeginQuery(t, q); }
static void w_glEndQuery(GLenum t) { p_glEndQuery(t); }
static GLint w_glGetQueryObjectiv(GLuint q, GLenum p) { GLint v = 0; p_glGetQueryObjectiv(q, p, &v); return v; }
static GLuint w_glGenFramebuffer(void) { GLuint v = 0; p_glGenFramebuffers(1, &v); return v; }
static void w_glBindFramebuffer(GLenum t, GLuint f) { p_glBindFramebuffer(t, f); }
static void w_glFramebufferTexture2D(GLenum t, GLenum a, GLenum tt, GLuint tex, GLint l) { p_glFramebufferTexture2D(t, a, tt, tex, l); }
static GLenum w_glCheckFramebufferStatus(GLenum t) { return p_glCheckFramebufferStatus(t); }
static void w_glDeleteFramebuffer(GLuint f) { p_glDeleteFramebuffers(1, &f); }
static void w_glDeleteTexture(GLuint t) { p_glDeleteTextures(1, &t); }
static uintptr_t w_glFenceSync(void) { return (uintptr_t)p_glFenceSync(0x9117, 0); }
static void w_glDeleteSync(uintptr_t s) { p_glDeleteSync((void*)s); }
static void w_glUniform1f(GLint l, GLfloat a) { p_glUniform1f(l, a); }
static void w_glUniform4f(GLint l, GLfloat a, GLfloat b, GLfloat c, GLfloat d) { p_glUniform4f(l, a, b, c, d); }
static void w_glTexSubImage2D(GLenum t, GLint l, GLint x, GLint y, GLsizei w, GLsizei h, GLenum f, GLenum ty, const void* d) {
	p_glTexSubImage2D(t, l, x, y, w, h, f, ty, d);
}
static void w_glDrawArrays(GLenum m, GLint f, GLsizei c) { p_glDrawArrays(m, f, c); }
static void w_glReadPixels(GLint x, GLint y, GLsizei w, GLsizei h, GLenum f, GLenum t, void* d) { p_glReadPixels(x, y, w, h, f, t, d); }
static GLuint64 w_glGetQueryObjectui64v(GLuint q, GLenum p) { GLuint64 v = 0; p_glGetQueryObjectui64v(q, p, &v); return v; }
*/
import "C"

import (
	"errors"
	"unsafe"
)

// GL enums used by the spike.
const (
	VENDOR                   = 0x1F00
	RENDERER                 = 0x1F01
	VERSION                  = 0x1F02
	SHADING_LANGUAGE_VERSION = 0x8B8C
	VIEWPORT                 = 0x0BA2
	FRAMEBUFFER_BINDING      = 0x8CA6
	COLOR_BUFFER_BIT         = 0x00004000
	BLEND                    = 0x0BE2
	ONE                      = 1
	ONE_MINUS_SRC_ALPHA      = 0x0303
	ARRAY_BUFFER             = 0x8892
	STREAM_DRAW              = 0x88E0
	DYNAMIC_DRAW             = 0x88E8
	STATIC_DRAW              = 0x88E4
	UNSIGNED_BYTE            = 0x1401
	UNSIGNED_INT             = 0x1405
	FLOAT                    = 0x1406
	VERTEX_SHADER            = 0x8B31
	FRAGMENT_SHADER          = 0x8B30
	COMPILE_STATUS           = 0x8B81
	LINK_STATUS              = 0x8B82
	INFO_LOG_LENGTH          = 0x8B84
	TEXTURE_2D               = 0x0DE1
	TEXTURE0                 = 0x84C0
	R8                       = 0x8229
	RED                      = 0x1903
	RGBA                     = 0x1908
	PACK_ALIGNMENT           = 0x0D05
	RGBA8                    = 0x8058
	FRAMEBUFFER              = 0x8D40
	COLOR_ATTACHMENT0        = 0x8CE0
	FRAMEBUFFER_COMPLETE     = 0x8CD5
	TRIANGLES                = 0x0004
	TEXTURE_MIN_FILTER       = 0x2801
	TEXTURE_MAG_FILTER       = 0x2800
	TEXTURE_WRAP_S           = 0x2802
	TEXTURE_WRAP_T           = 0x2803
	NEAREST                  = 0x2600
	CLAMP_TO_EDGE            = 0x812F
	UNPACK_ALIGNMENT         = 0x0CF5
	UNPACK_ROW_LENGTH        = 0x0CF2
	TRIANGLE_STRIP           = 0x0005
	TIME_ELAPSED             = 0x88BF
	QUERY_RESULT             = 0x8866
	QUERY_RESULT_AVAILABLE   = 0x8867
	FALSE                    = 0
	TRUE                     = 1
)

// Init resolves all entry points. Call it once with a GL context current.
func Init() error {
	if missing := C.glgrid_load(); missing != nil {
		return errors.New("gl: cannot resolve " + C.GoString(missing))
	}
	return nil
}

// LoaderName reports which loader resolved the entry points.
func LoaderName() string {
	if C.glgrid_has_egl() != 0 {
		return "dlopen(libEGL.so.1) + eglGetProcAddress"
	}
	return "dlopen(libOpenGL.so.0) + dlsym"
}

func GetString(name uint32) string {
	return C.GoString(C.w_glGetString(C.GLenum(name)))
}

func GetIntegerv(name uint32, out []int32) {
	C.w_glGetIntegerv(C.GLenum(name), (*C.GLint)(unsafe.Pointer(&out[0])))
}

func GetError() uint32                 { return uint32(C.w_glGetError()) }
func Viewport(x, y, w, h int32)        { C.w_glViewport(C.GLint(x), C.GLint(y), C.GLsizei(w), C.GLsizei(h)) }
func Clear(mask uint32)                { C.w_glClear(C.GLbitfield(mask)) }
func Enable(cap uint32)                { C.w_glEnable(C.GLenum(cap)) }
func Disable(cap uint32)               { C.w_glDisable(C.GLenum(cap)) }
func BlendFunc(src, dst uint32)        { C.w_glBlendFunc(C.GLenum(src), C.GLenum(dst)) }
func Flush()                           { C.w_glFlush() }
func Finish()                          { C.w_glFinish() }
func GenVertexArray() uint32           { return uint32(C.w_glGenVertexArray()) }
func BindVertexArray(v uint32)         { C.w_glBindVertexArray(C.GLuint(v)) }
func GenBuffer() uint32                { return uint32(C.w_glGenBuffer()) }
func BindBuffer(target, b uint32)      { C.w_glBindBuffer(C.GLenum(target), C.GLuint(b)) }
func EnableVertexAttribArray(i uint32) { C.w_glEnableVertexAttribArray(C.GLuint(i)) }
func VertexAttribDivisor(i, d uint32)  { C.w_glVertexAttribDivisor(C.GLuint(i), C.GLuint(d)) }
func UseProgram(p uint32)              { C.w_glUseProgram(C.GLuint(p)) }
func Uniform1i(loc, a int32)           { C.w_glUniform1i(C.GLint(loc), C.GLint(a)) }
func Uniform2i(loc, a, b int32)        { C.w_glUniform2i(C.GLint(loc), C.GLint(a), C.GLint(b)) }
func Uniform2f(loc int32, a, b float32) {
	C.w_glUniform2f(C.GLint(loc), C.GLfloat(a), C.GLfloat(b))
}
func GenTexture() uint32           { return uint32(C.w_glGenTexture()) }
func BindTexture(target, t uint32) { C.w_glBindTexture(C.GLenum(target), C.GLuint(t)) }
func ActiveTexture(t uint32)       { C.w_glActiveTexture(C.GLenum(t)) }
func TexParameteri(target, p uint32, v int32) {
	C.w_glTexParameteri(C.GLenum(target), C.GLenum(p), C.GLint(v))
}
func PixelStorei(p uint32, v int32) { C.w_glPixelStorei(C.GLenum(p), C.GLint(v)) }
func DrawArraysInstanced(mode uint32, first, count, instances int32) {
	C.w_glDrawArraysInstanced(C.GLenum(mode), C.GLint(first), C.GLsizei(count), C.GLsizei(instances))
}
func GenQuery() uint32            { return uint32(C.w_glGenQuery()) }
func BeginQuery(target, q uint32) { C.w_glBeginQuery(C.GLenum(target), C.GLuint(q)) }
func EndQuery(target uint32)      { C.w_glEndQuery(C.GLenum(target)) }
func GetQueryObjecti(q, p uint32) int32 {
	return int32(C.w_glGetQueryObjectiv(C.GLuint(q), C.GLenum(p)))
}
func GetQueryObjectui64(q, p uint32) uint64 {
	return uint64(C.w_glGetQueryObjectui64v(C.GLuint(q), C.GLenum(p)))
}

func ClearColor(r, g, b, a float32) {
	C.w_glClearColor(C.GLfloat(r), C.GLfloat(g), C.GLfloat(b), C.GLfloat(a))
}

// BufferData allocates (and optionally fills) the bound buffer. data may be nil.
func BufferData(target uint32, size int, data unsafe.Pointer, usage uint32) {
	C.w_glBufferData(C.GLenum(target), C.GLsizeiptr(size), data, C.GLenum(usage))
}

// BufferSubData copies size bytes from data into the bound buffer at offset.
// data must point to memory without Go pointers.
func BufferSubData(target uint32, offset, size int, data unsafe.Pointer) {
	C.w_glBufferSubData(C.GLenum(target), C.GLintptr(offset), C.GLsizeiptr(size), data)
}

func VertexAttribPointer(i uint32, n int32, typ uint32, normalized bool, stride int32, offset uintptr) {
	norm := C.GLboolean(FALSE)
	if normalized {
		norm = TRUE
	}
	C.w_glVertexAttribPointer(C.GLuint(i), C.GLint(n), C.GLenum(typ), norm, C.GLsizei(stride), C.uintptr_t(offset))
}

func VertexAttribIPointer(i uint32, n int32, typ uint32, stride int32, offset uintptr) {
	C.w_glVertexAttribIPointer(C.GLuint(i), C.GLint(n), C.GLenum(typ), C.GLsizei(stride), C.uintptr_t(offset))
}

// TexImage2D uploads a 2D texture level. data may be nil.
func TexImage2D(target uint32, level, internalFormat, w, h int32, format, typ uint32, data unsafe.Pointer) {
	C.w_glTexImage2D(C.GLenum(target), C.GLint(level), C.GLint(internalFormat), C.GLsizei(w), C.GLsizei(h),
		C.GLenum(format), C.GLenum(typ), data)
}

// CompileProgram builds and links a vertex+fragment program.
func CompileProgram(vertexSrc, fragmentSrc string) (uint32, error) {
	vs, err := compileShader(VERTEX_SHADER, vertexSrc)
	if err != nil {
		return 0, err
	}
	fs, err := compileShader(FRAGMENT_SHADER, fragmentSrc)
	if err != nil {
		return 0, err
	}
	p := C.w_glCreateProgram()
	C.w_glAttachShader(p, vs)
	C.w_glAttachShader(p, fs)
	C.w_glLinkProgram(p)
	C.w_glDeleteShader(vs)
	C.w_glDeleteShader(fs)
	if C.w_glGetProgramiv(p, LINK_STATUS) == 0 {
		n := C.w_glGetProgramiv(p, INFO_LOG_LENGTH)
		buf := (*C.char)(C.calloc(C.size_t(n+1), 1))
		defer C.free(unsafe.Pointer(buf))
		C.w_glGetProgramInfoLog(p, C.GLsizei(n), buf)
		return 0, errors.New("gl: link: " + C.GoString(buf))
	}
	return uint32(p), nil
}

func compileShader(kind uint32, src string) (C.GLuint, error) {
	s := C.w_glCreateShader(C.GLenum(kind))
	csrc := C.CString(src)
	defer C.free(unsafe.Pointer(csrc))
	C.w_glShaderSource(s, csrc)
	C.w_glCompileShader(s)
	if C.w_glGetShaderiv(s, COMPILE_STATUS) == 0 {
		n := C.w_glGetShaderiv(s, INFO_LOG_LENGTH)
		buf := (*C.char)(C.calloc(C.size_t(n+1), 1))
		defer C.free(unsafe.Pointer(buf))
		C.w_glGetShaderInfoLog(s, C.GLsizei(n), buf)
		return 0, errors.New("gl: compile: " + C.GoString(buf))
	}
	return s, nil
}

func GenFramebuffer() uint32            { return uint32(C.w_glGenFramebuffer()) }
func BindFramebuffer(target, fb uint32) { C.w_glBindFramebuffer(C.GLenum(target), C.GLuint(fb)) }
func FramebufferTexture2D(target, attachment, texTarget, tex uint32, level int32) {
	C.w_glFramebufferTexture2D(C.GLenum(target), C.GLenum(attachment), C.GLenum(texTarget), C.GLuint(tex), C.GLint(level))
}
func CheckFramebufferStatus(target uint32) uint32 {
	return uint32(C.w_glCheckFramebufferStatus(C.GLenum(target)))
}
func DeleteFramebuffer(fb uint32) { C.w_glDeleteFramebuffer(C.GLuint(fb)) }
func DeleteTexture(t uint32)      { C.w_glDeleteTexture(C.GLuint(t)) }

// FenceSync inserts a GL_SYNC_GPU_COMMANDS_COMPLETE fence and returns the
// GLsync handle as an integer (it is an opaque driver pointer).
func FenceSync() uintptr             { return uintptr(C.w_glFenceSync()) }
func DeleteSync(s uintptr)           { C.w_glDeleteSync(C.uintptr_t(s)) }
func Uniform1f(loc int32, v float32) { C.w_glUniform1f(C.GLint(loc), C.GLfloat(v)) }
func DrawArrays(mode uint32, first, count int32) {
	C.w_glDrawArrays(C.GLenum(mode), C.GLint(first), C.GLsizei(count))
}

func Uniform4f(loc int32, a, b, c, d float32) {
	C.w_glUniform4f(C.GLint(loc), C.GLfloat(a), C.GLfloat(b), C.GLfloat(c), C.GLfloat(d))
}

// TexSubImage2D updates a rectangle of the bound texture from data.
func TexSubImage2D(target uint32, level, x, y, w, h int32, format, typ uint32, data unsafe.Pointer) {
	C.w_glTexSubImage2D(C.GLenum(target), C.GLint(level), C.GLint(x), C.GLint(y), C.GLsizei(w), C.GLsizei(h),
		C.GLenum(format), C.GLenum(typ), data)
}

// ReadPixels reads a rectangle of the bound read framebuffer into data.
func ReadPixels(x, y, w, h int32, format, typ uint32, data unsafe.Pointer) {
	C.w_glReadPixels(C.GLint(x), C.GLint(y), C.GLsizei(w), C.GLsizei(h), C.GLenum(format), C.GLenum(typ), data)
}

// UniformLocation looks up a uniform by name (init-time only; allocates).
func UniformLocation(program uint32, name string) int32 {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	return int32(C.w_glGetUniformLocation(C.GLuint(program), cname))
}
