# Android compiler reuse across independent checkouts

Checked 2026-10-16 against primary upstream documentation and source. A local
qualification of a reference Android application found useful Gradle task reuse
and no C++ compiler hits. The next compiler experiment should explain its input
differences before adding another storage transport.

## What the local pair established

The producer and consumer used independent checkouts and Gradle homes. The
consumer started without task outputs or a populated Gradle task cache. It used
the direct private Gradle HTTP endpoint and an explicitly copied warm ccache
seed. Dependency downloads were seeded separately.

| Observation | Result |
| --- | --- |
| Cold Android compilation command | 880.213 seconds |
| Independent consumer command | 524.971 seconds |
| Consumer Gradle task outcomes | 318 `FROM-CACHE` |
| Compared restored output files | 290 exact matches |
| Compared entries inside the APK | 13 exact matches |
| Consumer ccache hits | 0 of 94 cacheable calls |
| Uncacheable calls in each build | 211 of 305 calls |
| PCH refusals in each build | 206 of 305 calls |
| Link calls in each build | 5 of 305 calls |

Each build had 94 cacheable compiler misses and 211 uncacheable calls. Those
uncacheable calls comprised 206 PCH refusals and five link calls. The copied
compiler seed therefore contributed no observed compiler hits. Keep this result
separate from the Gradle task outcomes.

This was one local producer/consumer pair, with compilation and packaging only.
It did not run an emulator, prove current application behavior, measure hosted
CI allocation or establish a price saving. The complete APK was not compared
byte for byte. Timings include the work of the measured commands; they are not
a controlled estimate of the isolated Gradle cache contribution. Repeated runs
with equivalent resources and measured setup and transfer costs remain needed.

## Input differences worth testing

The reference application sets `CCACHE_BASEDIR` to the checkout and
`CCACHE_COMPILERCHECK=content`, with a bounded local compiler cache. Its generated
x86_64 Debug compilation databases contain `-g` without debug or file prefix
maps. The CMake working directories differ between checkouts. Sampled commands
also include prefab headers from different Gradle homes, outside the checkout
base directory.

With Debug output, ccache 4.9.1 hashes the working directory to preserve debug
paths. Its base-directory setting rewrites paths only inside that base. Compiler
prefix mapping can normalize debug output; disabling directory hashing instead
can return incorrect debug locations. The manual also warns that relative-path
rewriting can affect dependency files. [Pinned ccache path contract](https://ccache.dev/manual/4.9.1.html#_compiling_in_different_directories).

The pinned implementation applies debug prefix maps to the working directory
before hashing it. This makes the differing working directories a specific
hypothesis to test. It does not establish that they explain every miss.
[ccache 4.9.1 implementation](https://github.com/ccache/ccache/blob/v4.9.1/src/ccache.cpp#L1483).

The sampled commands select NDK 27.1.12297006 and Clang 18.0.2. NDK
27.0.12077973 uses Clang 18.0.1 and is a separate toolchain identity. Preserve
the selected compiler and SDK context when comparing results.

## PCH needs its own correctness proof

Reanimated 4.6.0 and Worklets 0.12.2 use PCH in the observed build. Their CMake
commands already contain `-Xclang -fno-pch-timestamp`. Adding that flag alone
does not address these refusals.

For PCH, ccache 4.9.1 requires relaxed macro and time checking, as well as the
Clang timestamp option. Its argument processor rejects PCH consumption without
`time_macros` and PCH production without `pch_defines`. These refusals preserve
an explicit correctness boundary. [Pinned requirements](https://ccache.dev/manual/4.9.1.html#_precompiled_headers),
[argument processing](https://github.com/ccache/ccache/blob/v4.9.1/src/argprocessing.cpp#L1311).

The ccache 4.14.1 manual retains those requirements. An upgrade alone is not an
established fix for complete input tracking. [ccache 4.14.1 PCH contract](https://ccache.dev/manual/4.14.1.html#_precompiled_headers).

Do not enable ignored headers, ignored compiler flags, disabled directory
hashing or PCH sloppiness merely to increase hit counts. Personal Cache and Team
Cache must retain the native client's full build context.

## A small next experiment

These steps are proposed and were not part of the measured pair. Start with one
small translation unit that does not use PCH, rather than another full Android
build.

1. Replay its existing compile command in a disposable cache with
   `CCACHE_DEBUG=1`. Redirect the object, dependency file and any other outputs
   to scratch data. Confirm that a repeat within the same checkout hits, then
   compare private per-object hashed-input records between checkouts.
2. Keep directory hashing enabled and test an explicit debug prefix map from
   each physical source root to one logical root. Inspect the first remaining
   input difference, especially external prefab paths and preprocessor line
   markers. A source-root map may leave the Gradle-home difference intact.
3. Test fixed logical source and Gradle-home roots in isolated execution
   environments backed by independent physical directories. Compare the cached
   consumer with an uncached consumer under the same profile. Require matching
   object contents and usable dependency paths.
4. Build a separate tiny PCH fixture with the selected NDK. Change a transitive
   header, macro value, time macro and PCH contents independently. Require
   correct misses, fresh execution or changed outputs before adopting any
   compiler-cache configuration.

For the pinned ccache, `-fdebug-prefix-map` is the first mapping candidate
because its source handles it explicitly. A broader `-ffile-prefix-map` also
changes `__FILE__` behavior. Treat either as an explicit build-profile change
with qualified debugger and source-mapping behavior. LayerCache should not
inject it silently or promise equivalence with the previous profile.
[Pinned prefix-map handling](https://github.com/ccache/ccache/blob/v4.9.1/src/argprocessing.cpp#L619).

After the small experiment passes, repeat the independent Android pair and
measure compiler hits, refusals, transfer bytes and complete command duration.
Keep Gradle and compiler results separate.

## Where sccache and upstream work fit

The inspected sccache source is
`7bdb9bdcde435fd365e291715a6003f3ca28c2f9`, an upstream main snapshot rather than
a qualified release pin. Its C/C++ keys include compiler identity, arguments,
environment and preprocessed output. [Official hashing contract](https://github.com/mozilla/sccache/blob/7bdb9bdcde435fd365e291715a6003f3ca28c2f9/docs/Caching.md).

Its Clang parser recognizes `-include-pch`, `-emit-pch` and
`-fno-pch-timestamp`, with parser tests. Recognizing arguments does not prove
portable PCH reuse or correct invalidation for the proposed controls.
[Pinned parser and tests](https://github.com/mozilla/sccache/blob/7bdb9bdcde435fd365e291715a6003f3ca28c2f9/src/compiler/clang.rs#L224).

`SCCACHE_BASEDIRS` normalizes paths for hashing. That operation does not rewrite
the compiler's actual debug or macro output. Qualify an explicit output profile
and compare against uncached consumer output before accepting cross-checkout
hits. [Official normalization contract](https://github.com/mozilla/sccache/blob/7bdb9bdcde435fd365e291715a6003f3ca28c2f9/README.md),
[hash computation](https://github.com/mozilla/sccache/blob/7bdb9bdcde435fd365e291715a6003f3ca28c2f9/src/compiler/c.rs#L1608).

A useful upstream contribution would be the minimal NDK/Clang PCH fixture with
independent roots and header, macro, time and failure controls. Submit a bug
when it demonstrates an incorrect result or an unsupported valid invocation.
The present observations do not establish an upstream correctness bug.

For LayerCache, first improve diagnostics that distinguish uncacheable calls,
different input keys, absent entries, eviction and transfer failure. Keep
compiler hashing and execution in the upstream tool. Qualify reusable compiler
objects before adding ccache's native HTTP storage transport. More storage
cannot make an unsupported invocation reusable.
