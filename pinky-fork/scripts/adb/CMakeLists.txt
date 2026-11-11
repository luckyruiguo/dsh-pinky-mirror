# Builds adb, and nothing else, from the source of android-tools
# (https://github.com/nmeum/android-tools, AOSP's adb with a CMake build),
# for scripts/adb.sh. It follows android-tools' vendor/CMakeLists*.txt,
# with the Windows (mingw-w64) parts of MSYS2's mingw-w64-android-tools
# package, and links every library statically but the system's own.
#
#   cmake -S scripts/adb -B <build> -DVENDOR=<android-tools>/vendor \
#     -DPROTOBUF=<prefix> -DPROTOC=<protoc> -DZLIB_SRC=<dir> -DBROTLI_SRC=<dir> \
#     -DLZ4_SRC=<dir> -DZSTD_SRC=<dir> [-DADBWINAPI=<dir>] -DADB_VERSION=<version>
#
# PROTOBUF is the prefix of a static libprotobuf, and PROTOC the protoc of
# the same version, for the machine that builds (which is another one when
# cross-compiling). zlib (but on macOS, which has its own), brotli, lz4 and
# zstd are built here from their source folders. On Windows, ADBWINAPI is
# the folder with AOSP's adb_api.h and AdbWinApi.def.
#
# Without mDNS (android-tools' ANDROID_TOOLS_ADB_ENABLE_MDNS, which needs
# Rust): Pinky looks for the phones on the network itself and pairs and
# connects by address.
cmake_minimum_required(VERSION 3.22)
project(pinky-adb C CXX)

foreach(v VENDOR PROTOBUF PROTOC BROTLI_SRC LZ4_SRC ZSTD_SRC)
	if(NOT ${v})
		message(FATAL_ERROR "set -D${v}")
	endif()
endforeach()
if(WIN32 AND NOT ADBWINAPI)
	message(FATAL_ERROR "set -DADBWINAPI")
endif()
if(NOT APPLE AND NOT ZLIB_SRC)
	message(FATAL_ERROR "set -DZLIB_SRC")
endif()
# The version adb reports ("Version 37.0.0p1-pinky") and the vendor.
set(ADB_VERSION "" CACHE STRING "the android-tools version")
set(ADB_VENDOR pinky)

set(V ${VENDOR})

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_EXTENSIONS ON)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_EXTENSIONS ON)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

if(APPLE)
	add_compile_definitions(_DARWIN_C_SOURCE __DARWIN_C_LEVEL=__DARWIN_C_FULL)
endif()
# For C and C++ only: BoringSSL's Windows assembly goes through nasm.
include(CheckCXXCompilerFlag)
check_cxx_compiler_flag("-ftrivial-auto-var-init=zero" HAVE_TRIVIAL_ZERO_INIT)
if(HAVE_TRIVIAL_ZERO_INIT)
	add_compile_options($<$<COMPILE_LANGUAGE:C,CXX>:-ftrivial-auto-var-init=zero>)
endif()
add_compile_options($<$<COMPILE_LANGUAGE:C,CXX>:-Wno-attributes>)
add_compile_definitions(_FILE_OFFSET_BITS=64 _LARGEFILE64_SOURCE)

# Version headers, as android-tools writes them.
file(WRITE ${CMAKE_BINARY_DIR}/gen/build/version.h
	"#ifndef BUILD_VERSION_H\n#define BUILD_VERSION_H\n#include <string>\n"
	"namespace android { namespace build { std::string GetBuildNumber() { return \"${ADB_VENDOR}\"; } } }\n#endif\n")
file(WRITE ${CMAKE_BINARY_DIR}/gen/platform_tools_version.h
	"#define PLATFORM_TOOLS_VERSION \"${ADB_VERSION}\"\n")
# libziparchive's zip_writer.h wants googletest's FRIEND_TEST only.
file(WRITE ${CMAKE_BINARY_DIR}/gen/gtest/gtest_prod.h
	"#pragma once\n#define FRIEND_TEST(test_case_name, test_name) friend class test_case_name##_##test_name##_Test\n")
include_directories(${CMAKE_BINARY_DIR}/gen)

set(BUILD_TESTING OFF CACHE BOOL "" FORCE)
add_subdirectory(${V}/boringssl boringssl EXCLUDE_FROM_ALL)
add_subdirectory(${V}/fmtlib fmtlib EXCLUDE_FROM_ALL)

set(THREADS_PREFER_PTHREAD_FLAG ON)
find_package(Threads REQUIRED)

# protobuf, built by scripts/adb.sh.
add_library(dep_protobuf STATIC IMPORTED)
set_target_properties(dep_protobuf PROPERTIES IMPORTED_LOCATION ${PROTOBUF}/lib/libprotobuf.a
	INTERFACE_INCLUDE_DIRECTORIES ${PROTOBUF}/include)

# The compression libraries, from their sources.
if(APPLE)
	set(ZLIB z) # the system's
else()
	file(GLOB zlib_SOURCES ${ZLIB_SRC}/*.c)
	add_library(zlib STATIC ${zlib_SOURCES})
	target_include_directories(zlib PUBLIC ${ZLIB_SRC})
	set(ZLIB zlib)
endif()
file(GLOB brotli_SOURCES ${BROTLI_SRC}/c/common/*.c ${BROTLI_SRC}/c/dec/*.c ${BROTLI_SRC}/c/enc/*.c)
add_library(brotli STATIC ${brotli_SOURCES})
target_include_directories(brotli PUBLIC ${BROTLI_SRC}/c/include)
add_library(lz4 STATIC ${LZ4_SRC}/lib/lz4.c ${LZ4_SRC}/lib/lz4hc.c ${LZ4_SRC}/lib/lz4frame.c ${LZ4_SRC}/lib/xxhash.c)
target_include_directories(lz4 PUBLIC ${LZ4_SRC}/lib)
file(GLOB zstd_SOURCES ${ZSTD_SRC}/lib/common/*.c ${ZSTD_SRC}/lib/compress/*.c ${ZSTD_SRC}/lib/decompress/*.c)
add_library(zstd STATIC ${zstd_SOURCES})
# Without zstd's x86 assembly, which is for some systems only.
target_compile_definitions(zstd PRIVATE ZSTD_DISABLE_ASM)
target_include_directories(zstd PUBLIC ${ZSTD_SRC}/lib)

# adb's protocol buffers, generated with PROTOC.
set(PROTO_SRCS)
foreach(p app_processes adb_host key_type adb_known_hosts pairing)
	set(out ${CMAKE_BINARY_DIR}/gen/${p}.pb.cc ${CMAKE_BINARY_DIR}/gen/${p}.pb.h)
	add_custom_command(OUTPUT ${out}
		COMMAND ${PROTOC} --cpp_out=${CMAKE_BINARY_DIR}/gen -I${V}/adb/proto ${V}/adb/proto/${p}.proto
		DEPENDS ${V}/adb/proto/${p}.proto)
	list(APPEND PROTO_SRCS ${out})
endforeach()
# One copy of them, which every target that includes their headers links.
add_library(adb_proto STATIC ${PROTO_SRCS})
target_include_directories(adb_proto PUBLIC ${CMAKE_BINARY_DIR}/gen)
target_link_libraries(adb_proto PUBLIC dep_protobuf)

# libusb, as android-tools' CMakeLists.libusb.txt builds it.
set(libusb_SOURCES
	${V}/libusb/libusb/core.c
	${V}/libusb/libusb/descriptor.c
	${V}/libusb/libusb/hotplug.c
	${V}/libusb/libusb/io.c
	${V}/libusb/libusb/sync.c
	${V}/libusb/libusb/strerror.c)
if(WIN32)
	list(APPEND libusb_SOURCES
		${V}/libusb/libusb/os/events_windows.c
		${V}/libusb/libusb/os/threads_windows.c
		${V}/libusb/libusb/os/windows_common.c
		${V}/libusb/libusb/os/windows_hotplug.c
		${V}/libusb/libusb/os/windows_usbdk.c
		${V}/libusb/libusb/os/windows_winusb.c)
	set(libusb_CONFIG ${V}/libusb/windows)
elseif(APPLE)
	list(APPEND libusb_SOURCES
		${V}/libusb/libusb/os/events_posix.c
		${V}/libusb/libusb/os/darwin_usb.c
		${V}/libusb/libusb/os/threads_posix.c)
	set(libusb_CONFIG ${V}/libusb/darwin)
else()
	list(APPEND libusb_SOURCES
		${V}/libusb/libusb/os/events_posix.c
		${V}/libusb/libusb/os/linux_usbfs.c
		${V}/libusb/libusb/os/linux_netlink.c
		${V}/libusb/libusb/os/threads_posix.c)
	set(libusb_CONFIG ${V}/libusb/linux)
endif()
add_library(libusb STATIC ${libusb_SOURCES})
target_include_directories(libusb PRIVATE ${libusb_CONFIG} ${V}/libusb/libusb ${V}/libusb/libusb/os)
if(APPLE)
	target_link_libraries(libusb PRIVATE "-framework CoreFoundation" "-framework IOKit" "-framework Security" -lobjc)
endif()

add_library(libbase STATIC
	${V}/libbase/chrono_utils.cpp
	${V}/libbase/file.cpp
	${V}/libbase/hex.cpp
	${V}/libbase/logging.cpp
	${V}/libbase/mapped_file.cpp
	${V}/libbase/parsebool.cpp
	${V}/libbase/parsenetaddress.cpp
	${V}/libbase/posix_strerror_r.cpp
	${V}/libbase/process.cpp
	${V}/libbase/properties.cpp
	${V}/libbase/result.cpp
	${V}/libbase/stringprintf.cpp
	${V}/libbase/strings.cpp
	${V}/libbase/threads.cpp)
if(WIN32)
	target_sources(libbase PRIVATE ${V}/libbase/errors_windows.cpp ${V}/libbase/utf8.cpp)
	target_compile_definitions(libbase PUBLIC _POSIX_C_SOURCE)
else()
	target_sources(libbase PRIVATE ${V}/libbase/cmsg.cpp ${V}/libbase/errors_unix.cpp)
endif()
target_include_directories(libbase PUBLIC ${V}/libbase/include ${V}/core/include)
target_link_libraries(libbase PUBLIC fmt::fmt liblog)

add_library(liblog STATIC
	${V}/logging/liblog/log_event_list.cpp
	${V}/logging/liblog/log_event_write.cpp
	${V}/logging/liblog/logger_name.cpp
	${V}/logging/liblog/logger_read.cpp
	${V}/logging/liblog/logger_write.cpp
	${V}/logging/liblog/properties.cpp
	${V}/logging/liblog/logprint.cpp)
target_compile_definitions(liblog PRIVATE LIBLOG_LOG_TAG=1006 _XOPEN_SOURCE=700 FAKE_LOG_DEVICE=1)
if(WIN32)
	target_compile_definitions(liblog PRIVATE WIN32_LEAN_AND_MEAN)
endif()
target_include_directories(liblog PUBLIC
	${V}/core/include ${V}/logging/liblog/include ${V}/core/libcutils/include ${V}/libbase/include)

add_library(libcutils STATIC
	${V}/core/libcutils/config_utils.cpp
	${V}/core/libcutils/iosched_policy.cpp
	${V}/core/libcutils/load_file.cpp
	${V}/core/libcutils/native_handle.cpp
	${V}/core/libcutils/properties.cpp
	${V}/core/libcutils/record_stream.cpp
	${V}/core/libcutils/sockets.cpp
	${V}/core/libcutils/strlcpy.c)
if(WIN32)
	target_sources(libcutils PRIVATE
		${V}/core/libcutils/socket_inaddr_any_server_windows.cpp
		${V}/core/libcutils/socket_network_client_windows.cpp
		${V}/core/libcutils/sockets_windows.cpp)
else()
	target_sources(libcutils PRIVATE
		${V}/core/libcutils/android_get_control_file.cpp
		${V}/core/libcutils/ashmem-host.cpp
		${V}/core/libcutils/fs.cpp
		${V}/core/libcutils/hashmap.cpp
		${V}/core/libcutils/multiuser.cpp
		${V}/core/libcutils/socket_inaddr_any_server_unix.cpp
		${V}/core/libcutils/socket_local_client_unix.cpp
		${V}/core/libcutils/socket_local_server_unix.cpp
		${V}/core/libcutils/socket_network_client_unix.cpp
		${V}/core/libcutils/sockets_utils.cpp
		${V}/core/libcutils/sockets_unix.cpp
		${V}/core/libcutils/str_parms.cpp
		${V}/core/libcutils/trace-host.cpp)
	if(NOT APPLE)
		target_sources(libcutils PRIVATE
			${V}/core/libcutils/canned_fs_config.cpp
			${V}/core/libcutils/fs_config.cpp)
	endif()
endif()
target_compile_definitions(libcutils PRIVATE _GNU_SOURCE)
target_include_directories(libcutils PRIVATE ${V}/logging/liblog/include ${V}/core/libutils/include)
target_include_directories(libcutils PUBLIC ${V}/core/libcutils/include ${V}/libbase/include)

add_library(libzip STATIC
	${V}/libziparchive/zip_archive.cc
	${V}/libziparchive/zip_archive_stream_entry.cc
	${V}/libziparchive/zip_cd_entry_map.cc
	${V}/libziparchive/zip_error.cpp
	${V}/libziparchive/zip_writer.cc)
target_compile_definitions(libzip PRIVATE ZLIB_CONST)
target_include_directories(libzip PUBLIC
	${V}/libziparchive/include ${V}/libziparchive/incfs_support/include
	${V}/core/include ${V}/logging/liblog/include ${V}/libbase/include)
target_link_libraries(libzip PRIVATE ${ZLIB})

add_library(libadb STATIC
	${V}/adb/client/adb_client.cpp
	${V}/adb/client/adb_install.cpp
	${V}/adb/client/adb_wifi.cpp
	${V}/adb/client/auth.cpp
	${V}/adb/client/bugreport.cpp
	${V}/adb/client/commandline.cpp
	${V}/adb/client/console.cpp
	${V}/adb/client/detach.cpp
	${V}/adb/client/discovered_services.cpp
	${V}/adb/client/file_sync_client.cpp
	${V}/adb/client/incremental.cpp
	${V}/adb/client/incremental_server.cpp
	${V}/adb/client/incremental_utils.cpp
	${V}/adb/client/line_printer.cpp
	${V}/adb/client/main.cpp
	${V}/adb/client/pairing/pairing_client.cpp
	${V}/adb/client/usb_libusb.cpp
	${V}/adb/client/usb_libusb_device.cpp
	${V}/adb/client/usb_libusb_hotplug.cpp
	${V}/adb/client/usb_libusb_inhouse_hotplug.cpp
	${V}/adb/client/transport_emulator.cpp
	${V}/adb/client/mdns_tracker.cpp
	${V}/adb/client/mdns_utils.cpp
	${V}/adb/client/transport_mdns.cpp
	${V}/adb/client/transport_usb.cpp
	${V}/adb/pairing_auth/aes_128_gcm.cpp
	${V}/adb/pairing_auth/pairing_auth.cpp
	${V}/adb/pairing_connection/pairing_connection.cpp
	${V}/adb/services.cpp
	${V}/adb/socket_spec.cpp
	${V}/adb/sysdeps/errno.cpp)
if(WIN32)
	target_sources(libadb PRIVATE
		${V}/adb/client/usb_windows.cpp
		${V}/adb/sysdeps_win32.cpp
		${V}/adb/sysdeps/win32/errno.cpp
		${V}/adb/sysdeps/win32/stat.cpp)
	target_compile_definitions(libadb PRIVATE UNICODE _UNICODE _POSIX _POSIX_SOURCE)
	target_include_directories(libadb PUBLIC ${ADBWINAPI})
elseif(APPLE)
	target_sources(libadb PRIVATE
		${V}/adb/client/usb_osx.cpp
		${V}/adb/sysdeps_unix.cpp
		${V}/adb/sysdeps/posix/network.cpp)
else()
	target_sources(libadb PRIVATE
		${V}/adb/client/usb_linux.cpp
		${V}/adb/client/usb_linux_netlink.cpp
		${V}/adb/sysdeps_unix.cpp
		${V}/adb/sysdeps/posix/network.cpp)
endif()
target_compile_definitions(libadb PRIVATE _GNU_SOURCE)
target_compile_definitions(libadb PUBLIC ADB_HOST=1 ANDROID_TOOLS_USE_BUNDLED_LIBUSB)
target_include_directories(libadb PUBLIC
	${V}/boringssl/include
	${V}/adb
	${V}/adb/crypto/include
	${V}/adb/pairing_auth/include
	${V}/adb/pairing_connection/include
	${V}/adb/tls/include
	${V}/core/include
	${V}/core/libcrypto_utils/include
	${V}/core/libcutils/include
	${V}/libbase/include
	${V}/libziparchive/include
	${V}/libusb)
target_link_libraries(libadb PUBLIC fmt::fmt adb_proto brotli lz4 zstd)

add_library(libadb_crypto_defaults STATIC
	${V}/adb/crypto/key.cpp
	${V}/adb/crypto/rsa_2048_key.cpp
	${V}/adb/crypto/x509_generator.cpp)
target_include_directories(libadb_crypto_defaults PUBLIC
	${V}/adb ${V}/adb/crypto/include ${V}/boringssl/include
	${V}/core/libcrypto_utils/include ${V}/libbase/include)
target_link_libraries(libadb_crypto_defaults PUBLIC adb_proto)

add_library(libadb_tls_connection_defaults STATIC
	${V}/adb/tls/adb_ca_list.cpp
	${V}/adb/tls/tls_connection.cpp)
target_include_directories(libadb_tls_connection_defaults PUBLIC
	${V}/adb ${V}/adb/tls/include ${V}/boringssl/include ${V}/libbase/include)

add_library(libdiagnoseusb STATIC ${V}/core/diagnose_usb/diagnose_usb.cpp)
target_include_directories(libdiagnoseusb PUBLIC
	${V}/core/diagnose_usb/include ${V}/core/include ${V}/libbase/include)

add_library(libadb_sysdeps STATIC ${V}/adb/sysdeps/env.cpp)
target_include_directories(libadb_sysdeps PUBLIC ${V}/libbase/include ${V}/adb)

add_library(libcrypto_utils STATIC ${V}/core/libcrypto_utils/android_pubkey.cpp)
target_include_directories(libcrypto_utils PUBLIC ${V}/core/libcrypto_utils/include ${V}/boringssl/include)

add_executable(adb
	${V}/adb/adb.cpp
	${V}/adb/adb_io.cpp
	${V}/adb/adb_listeners.cpp
	${V}/adb/adb_trace.cpp
	${V}/adb/adb_unique_fd.cpp
	${V}/adb/adb_utils.cpp
	${V}/adb/fdevent/fdevent.cpp
	${V}/adb/fdevent/fdevent_poll.cpp
	${V}/adb/shell_service_protocol.cpp
	${V}/adb/sockets.cpp
	${V}/adb/socket_spec.cpp
	${V}/adb/transport.cpp
	${V}/adb/transport_fd.cpp
	${V}/adb/types.cpp)
if(WIN32)
	target_compile_definitions(adb PRIVATE _GNU_SOURCE _POSIX _POSIX_SOURCE)
else()
	target_sources(adb PRIVATE ${V}/adb/fdevent/fdevent_epoll.cpp)
endif()
target_include_directories(adb PRIVATE
	${V}/core/include ${V}/libbase/include ${V}/adb
	${V}/core/libcrypto_utils/include ${V}/boringssl/include)
target_link_libraries(adb
	libadb
	libadb_crypto_defaults
	libadb_tls_connection_defaults
	libbase
	libcrypto_utils
	libadb_sysdeps
	libcutils
	libdiagnoseusb
	liblog
	libzip
	crypto
	ssl
	adb_proto
	dep_protobuf
	brotli
	lz4
	zstd
	libusb
	${ZLIB}
	Threads::Threads)

if(APPLE)
	target_link_libraries(adb "-framework CoreFoundation" "-framework IOKit")
elseif(WIN32)
	# AOSP's AdbWinApi.dll, through an import library made from its .def.
	set(ADBWINAPI_LIBRARY ${CMAKE_BINARY_DIR}/AdbWinApi.dll.a)
	add_custom_command(OUTPUT ${ADBWINAPI_LIBRARY}
		COMMAND ${CMAKE_DLLTOOL} -d ${ADBWINAPI}/AdbWinApi.def -l ${ADBWINAPI_LIBRARY}
		DEPENDS ${ADBWINAPI}/AdbWinApi.def)
	add_custom_target(AdbWinApi DEPENDS ${ADBWINAPI_LIBRARY})
	add_dependencies(adb AdbWinApi)
	# Everything static, so that adb.exe needs only Windows' own DLLs and
	# AdbWinApi.dll; wmain is the entry point.
	target_link_libraries(adb ${ADBWINAPI_LIBRARY} ws2_32 userenv)
	target_link_options(adb PRIVATE -municode -static)
else()
	# A static executable (musl), which runs on any Linux.
	target_link_options(adb PRIVATE -static)
endif()
