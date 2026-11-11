//go:build android && cgo

package main

/*
#cgo LDFLAGS: -llog
#include <jni.h>
#include <stdlib.h>
#include <android/log.h>

static const char* pinky_chars(JNIEnv* env, jstring s) { return (*env)->GetStringUTFChars(env, s, NULL); }
static void pinky_release(JNIEnv* env, jstring s, const char* c) { (*env)->ReleaseStringUTFChars(env, s, c); }
static jstring pinky_string(JNIEnv* env, const char* c) { return (*env)->NewStringUTF(env, c); }
static void pinky_log(int prio, const char* msg) { __android_log_write(prio, "PinkyCore", msg); }
*/
import "C"

import (
	"strings"
	"unsafe"
)

// version is set by scripts/standalone.sh (-ldflags -X main.version=...).
var version = "dev"

//export Java_app_pinky_standalone_Core_nativeStart
func Java_app_pinky_standalone_Core_nativeStart(env *C.JNIEnv, _ C.jclass, dataDir, companion, core C.jstring) C.jstring {
	msg := ""
	if err := start(goString(env, dataDir), goString(env, companion), goString(env, core), version, logcat{}); err != nil {
		msg = err.Error()
	}
	cs := C.CString(msg)
	defer C.free(unsafe.Pointer(cs))
	return C.pinky_string(env, cs)
}

func goString(env *C.JNIEnv, s C.jstring) string {
	c := C.pinky_chars(env, s)
	defer C.pinky_release(env, s, c)
	return C.GoString(c)
}

// logcat writes each line to Android's log under the tag PinkyCore.
type logcat struct{}

func (logcat) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		prio := C.int(C.ANDROID_LOG_INFO)
		switch {
		case strings.Contains(line, "level=ERROR"):
			prio = C.ANDROID_LOG_ERROR
		case strings.Contains(line, "level=WARN"):
			prio = C.ANDROID_LOG_WARN
		case strings.Contains(line, "level=DEBUG"):
			prio = C.ANDROID_LOG_DEBUG
		}
		cs := C.CString(line)
		C.pinky_log(prio, cs)
		C.free(unsafe.Pointer(cs))
	}
	return len(p), nil
}
