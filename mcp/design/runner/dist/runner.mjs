// @bun
var __require = import.meta.require;

// node_modules/replicad-opencascadejs/src/replicad_single.js
var __dirname = "/private/tmp/design-release-0.4.0.F14bsc/mcp/design/runner/node_modules/replicad-opencascadejs/src", __filename = "/private/tmp/design-release-0.4.0.F14bsc/mcp/design/runner/node_modules/replicad-opencascadejs/src/replicad_single.js";
var Module = (() => {
  var _scriptDir = typeof document !== "undefined" && document.currentScript ? document.currentScript.src : undefined;
  if (typeof __filename !== "undefined")
    _scriptDir = _scriptDir || __filename;
  return function(Module2) {
    Module2 = Module2 || {};
    var Module2 = typeof Module2 != "undefined" ? Module2 : {};
    var readyPromiseResolve, readyPromiseReject;
    Module2["ready"] = new Promise(function(resolve, reject) {
      readyPromiseResolve = resolve;
      readyPromiseReject = reject;
    });
    var moduleOverrides = Object.assign({}, Module2);
    var arguments_ = [];
    var thisProgram = "./this.program";
    var quit_ = (status, toThrow) => {
      throw toThrow;
    };
    var ENVIRONMENT_IS_WEB = typeof window == "object";
    var ENVIRONMENT_IS_WORKER = typeof importScripts == "function";
    var ENVIRONMENT_IS_NODE = typeof process == "object" && typeof process.versions == "object" && typeof process.versions.node == "string";
    var scriptDirectory = "";
    function locateFile(path) {
      if (Module2["locateFile"]) {
        return Module2["locateFile"](path, scriptDirectory);
      }
      return scriptDirectory + path;
    }
    var read_, readAsync, readBinary, setWindowTitle;
    function logExceptionOnExit(e) {
      if (e instanceof ExitStatus)
        return;
      let toLog = e;
      err("exiting due to exception: " + toLog);
    }
    var fs;
    var nodePath;
    var requireNodeFS;
    if (ENVIRONMENT_IS_NODE) {
      if (ENVIRONMENT_IS_WORKER) {
        scriptDirectory = __require("path").dirname(scriptDirectory) + "/";
      } else {
        scriptDirectory = __dirname + "/";
      }
      requireNodeFS = () => {
        if (!nodePath) {
          fs = __require("fs");
          nodePath = __require("path");
        }
      };
      read_ = function shell_read(filename, binary) {
        requireNodeFS();
        filename = nodePath["normalize"](filename);
        return fs.readFileSync(filename, binary ? undefined : "utf8");
      };
      readBinary = (filename) => {
        var ret = read_(filename, true);
        if (!ret.buffer) {
          ret = new Uint8Array(ret);
        }
        return ret;
      };
      readAsync = (filename, onload, onerror) => {
        requireNodeFS();
        filename = nodePath["normalize"](filename);
        fs.readFile(filename, function(err2, data) {
          if (err2)
            onerror(err2);
          else
            onload(data.buffer);
        });
      };
      if (process["argv"].length > 1) {
        thisProgram = process["argv"][1].replace(/\\/g, "/");
      }
      arguments_ = process["argv"].slice(2);
      process["on"]("uncaughtException", function(ex) {
        if (!(ex instanceof ExitStatus)) {
          throw ex;
        }
      });
      process["on"]("unhandledRejection", function(reason) {
        throw reason;
      });
      quit_ = (status, toThrow) => {
        if (keepRuntimeAlive()) {
          process["exitCode"] = status;
          throw toThrow;
        }
        logExceptionOnExit(toThrow);
        process["exit"](status);
      };
      Module2["inspect"] = function() {
        return "[Emscripten Module object]";
      };
    } else if (ENVIRONMENT_IS_WEB || ENVIRONMENT_IS_WORKER) {
      if (ENVIRONMENT_IS_WORKER) {
        scriptDirectory = self.location.href;
      } else if (typeof document != "undefined" && document.currentScript) {
        scriptDirectory = document.currentScript.src;
      }
      if (_scriptDir) {
        scriptDirectory = _scriptDir;
      }
      if (scriptDirectory.indexOf("blob:") !== 0) {
        scriptDirectory = scriptDirectory.substr(0, scriptDirectory.replace(/[?#].*/, "").lastIndexOf("/") + 1);
      } else {
        scriptDirectory = "";
      }
      {
        read_ = (url) => {
          var xhr = new XMLHttpRequest;
          xhr.open("GET", url, false);
          xhr.send(null);
          return xhr.responseText;
        };
        if (ENVIRONMENT_IS_WORKER) {
          readBinary = (url) => {
            var xhr = new XMLHttpRequest;
            xhr.open("GET", url, false);
            xhr.responseType = "arraybuffer";
            xhr.send(null);
            return new Uint8Array(xhr.response);
          };
        }
        readAsync = (url, onload, onerror) => {
          var xhr = new XMLHttpRequest;
          xhr.open("GET", url, true);
          xhr.responseType = "arraybuffer";
          xhr.onload = () => {
            if (xhr.status == 200 || xhr.status == 0 && xhr.response) {
              onload(xhr.response);
              return;
            }
            onerror();
          };
          xhr.onerror = onerror;
          xhr.send(null);
        };
      }
      setWindowTitle = (title) => document.title = title;
    } else {}
    var out = Module2["print"] || console.log.bind(console);
    var err = Module2["printErr"] || console.warn.bind(console);
    Object.assign(Module2, moduleOverrides);
    moduleOverrides = null;
    if (Module2["arguments"])
      arguments_ = Module2["arguments"];
    if (Module2["thisProgram"])
      thisProgram = Module2["thisProgram"];
    if (Module2["quit"])
      quit_ = Module2["quit"];
    var POINTER_SIZE = 4;
    var tempRet0 = 0;
    var setTempRet0 = (value) => {
      tempRet0 = value;
    };
    var wasmBinary;
    if (Module2["wasmBinary"])
      wasmBinary = Module2["wasmBinary"];
    var noExitRuntime = Module2["noExitRuntime"] || true;
    if (typeof WebAssembly != "object") {
      abort("no native wasm support detected");
    }
    var wasmMemory;
    var ABORT = false;
    var EXITSTATUS;
    function assert(condition, text) {
      if (!condition) {
        abort(text);
      }
    }
    var UTF8Decoder = typeof TextDecoder != "undefined" ? new TextDecoder("utf8") : undefined;
    function UTF8ArrayToString(heapOrArray, idx, maxBytesToRead) {
      var endIdx = idx + maxBytesToRead;
      var endPtr = idx;
      while (heapOrArray[endPtr] && !(endPtr >= endIdx))
        ++endPtr;
      if (endPtr - idx > 16 && heapOrArray.buffer && UTF8Decoder) {
        return UTF8Decoder.decode(heapOrArray.subarray(idx, endPtr));
      } else {
        var str = "";
        while (idx < endPtr) {
          var u0 = heapOrArray[idx++];
          if (!(u0 & 128)) {
            str += String.fromCharCode(u0);
            continue;
          }
          var u1 = heapOrArray[idx++] & 63;
          if ((u0 & 224) == 192) {
            str += String.fromCharCode((u0 & 31) << 6 | u1);
            continue;
          }
          var u2 = heapOrArray[idx++] & 63;
          if ((u0 & 240) == 224) {
            u0 = (u0 & 15) << 12 | u1 << 6 | u2;
          } else {
            u0 = (u0 & 7) << 18 | u1 << 12 | u2 << 6 | heapOrArray[idx++] & 63;
          }
          if (u0 < 65536) {
            str += String.fromCharCode(u0);
          } else {
            var ch = u0 - 65536;
            str += String.fromCharCode(55296 | ch >> 10, 56320 | ch & 1023);
          }
        }
      }
      return str;
    }
    function UTF8ToString(ptr, maxBytesToRead) {
      return ptr ? UTF8ArrayToString(HEAPU8, ptr, maxBytesToRead) : "";
    }
    function stringToUTF8Array(str, heap, outIdx, maxBytesToWrite) {
      if (!(maxBytesToWrite > 0))
        return 0;
      var startIdx = outIdx;
      var endIdx = outIdx + maxBytesToWrite - 1;
      for (var i2 = 0;i2 < str.length; ++i2) {
        var u = str.charCodeAt(i2);
        if (u >= 55296 && u <= 57343) {
          var u1 = str.charCodeAt(++i2);
          u = 65536 + ((u & 1023) << 10) | u1 & 1023;
        }
        if (u <= 127) {
          if (outIdx >= endIdx)
            break;
          heap[outIdx++] = u;
        } else if (u <= 2047) {
          if (outIdx + 1 >= endIdx)
            break;
          heap[outIdx++] = 192 | u >> 6;
          heap[outIdx++] = 128 | u & 63;
        } else if (u <= 65535) {
          if (outIdx + 2 >= endIdx)
            break;
          heap[outIdx++] = 224 | u >> 12;
          heap[outIdx++] = 128 | u >> 6 & 63;
          heap[outIdx++] = 128 | u & 63;
        } else {
          if (outIdx + 3 >= endIdx)
            break;
          heap[outIdx++] = 240 | u >> 18;
          heap[outIdx++] = 128 | u >> 12 & 63;
          heap[outIdx++] = 128 | u >> 6 & 63;
          heap[outIdx++] = 128 | u & 63;
        }
      }
      heap[outIdx] = 0;
      return outIdx - startIdx;
    }
    function stringToUTF8(str, outPtr, maxBytesToWrite) {
      return stringToUTF8Array(str, HEAPU8, outPtr, maxBytesToWrite);
    }
    function lengthBytesUTF8(str) {
      var len = 0;
      for (var i2 = 0;i2 < str.length; ++i2) {
        var u = str.charCodeAt(i2);
        if (u >= 55296 && u <= 57343)
          u = 65536 + ((u & 1023) << 10) | str.charCodeAt(++i2) & 1023;
        if (u <= 127)
          ++len;
        else if (u <= 2047)
          len += 2;
        else if (u <= 65535)
          len += 3;
        else
          len += 4;
      }
      return len;
    }
    var UTF16Decoder = typeof TextDecoder != "undefined" ? new TextDecoder("utf-16le") : undefined;
    function UTF16ToString(ptr, maxBytesToRead) {
      var endPtr = ptr;
      var idx = endPtr >> 1;
      var maxIdx = idx + maxBytesToRead / 2;
      while (!(idx >= maxIdx) && HEAPU16[idx])
        ++idx;
      endPtr = idx << 1;
      if (endPtr - ptr > 32 && UTF16Decoder) {
        return UTF16Decoder.decode(HEAPU8.subarray(ptr, endPtr));
      } else {
        var str = "";
        for (var i2 = 0;!(i2 >= maxBytesToRead / 2); ++i2) {
          var codeUnit = HEAP16[ptr + i2 * 2 >> 1];
          if (codeUnit == 0)
            break;
          str += String.fromCharCode(codeUnit);
        }
        return str;
      }
    }
    function stringToUTF16(str, outPtr, maxBytesToWrite) {
      if (maxBytesToWrite === undefined) {
        maxBytesToWrite = 2147483647;
      }
      if (maxBytesToWrite < 2)
        return 0;
      maxBytesToWrite -= 2;
      var startPtr = outPtr;
      var numCharsToWrite = maxBytesToWrite < str.length * 2 ? maxBytesToWrite / 2 : str.length;
      for (var i2 = 0;i2 < numCharsToWrite; ++i2) {
        var codeUnit = str.charCodeAt(i2);
        HEAP16[outPtr >> 1] = codeUnit;
        outPtr += 2;
      }
      HEAP16[outPtr >> 1] = 0;
      return outPtr - startPtr;
    }
    function lengthBytesUTF16(str) {
      return str.length * 2;
    }
    function UTF32ToString(ptr, maxBytesToRead) {
      var i2 = 0;
      var str = "";
      while (!(i2 >= maxBytesToRead / 4)) {
        var utf32 = HEAP32[ptr + i2 * 4 >> 2];
        if (utf32 == 0)
          break;
        ++i2;
        if (utf32 >= 65536) {
          var ch = utf32 - 65536;
          str += String.fromCharCode(55296 | ch >> 10, 56320 | ch & 1023);
        } else {
          str += String.fromCharCode(utf32);
        }
      }
      return str;
    }
    function stringToUTF32(str, outPtr, maxBytesToWrite) {
      if (maxBytesToWrite === undefined) {
        maxBytesToWrite = 2147483647;
      }
      if (maxBytesToWrite < 4)
        return 0;
      var startPtr = outPtr;
      var endPtr = startPtr + maxBytesToWrite - 4;
      for (var i2 = 0;i2 < str.length; ++i2) {
        var codeUnit = str.charCodeAt(i2);
        if (codeUnit >= 55296 && codeUnit <= 57343) {
          var trailSurrogate = str.charCodeAt(++i2);
          codeUnit = 65536 + ((codeUnit & 1023) << 10) | trailSurrogate & 1023;
        }
        HEAP32[outPtr >> 2] = codeUnit;
        outPtr += 4;
        if (outPtr + 4 > endPtr)
          break;
      }
      HEAP32[outPtr >> 2] = 0;
      return outPtr - startPtr;
    }
    function lengthBytesUTF32(str) {
      var len = 0;
      for (var i2 = 0;i2 < str.length; ++i2) {
        var codeUnit = str.charCodeAt(i2);
        if (codeUnit >= 55296 && codeUnit <= 57343)
          ++i2;
        len += 4;
      }
      return len;
    }
    function allocateUTF8(str) {
      var size = lengthBytesUTF8(str) + 1;
      var ret = _malloc(size);
      if (ret)
        stringToUTF8Array(str, HEAP8, ret, size);
      return ret;
    }
    function writeArrayToMemory(array, buffer2) {
      HEAP8.set(array, buffer2);
    }
    function writeAsciiToMemory(str, buffer2, dontAddNull) {
      for (var i2 = 0;i2 < str.length; ++i2) {
        HEAP8[buffer2++ >> 0] = str.charCodeAt(i2);
      }
      if (!dontAddNull)
        HEAP8[buffer2 >> 0] = 0;
    }
    var buffer, HEAP8, HEAPU8, HEAP16, HEAPU16, HEAP32, HEAPU32, HEAPF32, HEAPF64;
    function updateGlobalBufferAndViews(buf) {
      buffer = buf;
      Module2["HEAP8"] = HEAP8 = new Int8Array(buf);
      Module2["HEAP16"] = HEAP16 = new Int16Array(buf);
      Module2["HEAP32"] = HEAP32 = new Int32Array(buf);
      Module2["HEAPU8"] = HEAPU8 = new Uint8Array(buf);
      Module2["HEAPU16"] = HEAPU16 = new Uint16Array(buf);
      Module2["HEAPU32"] = HEAPU32 = new Uint32Array(buf);
      Module2["HEAPF32"] = HEAPF32 = new Float32Array(buf);
      Module2["HEAPF64"] = HEAPF64 = new Float64Array(buf);
    }
    var INITIAL_MEMORY = Module2["INITIAL_MEMORY"] || 16777216;
    var wasmTable;
    var __ATPRERUN__ = [];
    var __ATINIT__ = [];
    var __ATPOSTRUN__ = [];
    var runtimeInitialized = false;
    function keepRuntimeAlive() {
      return noExitRuntime;
    }
    function preRun() {
      if (Module2["preRun"]) {
        if (typeof Module2["preRun"] == "function")
          Module2["preRun"] = [Module2["preRun"]];
        while (Module2["preRun"].length) {
          addOnPreRun(Module2["preRun"].shift());
        }
      }
      callRuntimeCallbacks(__ATPRERUN__);
    }
    function initRuntime() {
      runtimeInitialized = true;
      if (!Module2["noFSInit"] && !FS.init.initialized)
        FS.init();
      FS.ignorePermissions = false;
      TTY.init();
      callRuntimeCallbacks(__ATINIT__);
    }
    function postRun() {
      if (Module2["postRun"]) {
        if (typeof Module2["postRun"] == "function")
          Module2["postRun"] = [Module2["postRun"]];
        while (Module2["postRun"].length) {
          addOnPostRun(Module2["postRun"].shift());
        }
      }
      callRuntimeCallbacks(__ATPOSTRUN__);
    }
    function addOnPreRun(cb) {
      __ATPRERUN__.unshift(cb);
    }
    function addOnInit(cb) {
      __ATINIT__.unshift(cb);
    }
    function addOnPostRun(cb) {
      __ATPOSTRUN__.unshift(cb);
    }
    var runDependencies = 0;
    var runDependencyWatcher = null;
    var dependenciesFulfilled = null;
    function getUniqueRunDependency(id) {
      return id;
    }
    function addRunDependency(id) {
      runDependencies++;
      if (Module2["monitorRunDependencies"]) {
        Module2["monitorRunDependencies"](runDependencies);
      }
    }
    function removeRunDependency(id) {
      runDependencies--;
      if (Module2["monitorRunDependencies"]) {
        Module2["monitorRunDependencies"](runDependencies);
      }
      if (runDependencies == 0) {
        if (runDependencyWatcher !== null) {
          clearInterval(runDependencyWatcher);
          runDependencyWatcher = null;
        }
        if (dependenciesFulfilled) {
          var callback = dependenciesFulfilled;
          dependenciesFulfilled = null;
          callback();
        }
      }
    }
    function abort(what) {
      {
        if (Module2["onAbort"]) {
          Module2["onAbort"](what);
        }
      }
      what = "Aborted(" + what + ")";
      err(what);
      ABORT = true;
      EXITSTATUS = 1;
      what += ". Build with -sASSERTIONS for more info.";
      var e = new WebAssembly.RuntimeError(what);
      readyPromiseReject(e);
      throw e;
    }
    var dataURIPrefix = "data:application/octet-stream;base64,";
    function isDataURI(filename) {
      return filename.startsWith(dataURIPrefix);
    }
    function isFileURI(filename) {
      return filename.startsWith("file://");
    }
    var wasmBinaryFile;
    wasmBinaryFile = "replicad_single.wasm";
    if (!isDataURI(wasmBinaryFile)) {
      wasmBinaryFile = locateFile(wasmBinaryFile);
    }
    function getBinary(file) {
      try {
        if (file == wasmBinaryFile && wasmBinary) {
          return new Uint8Array(wasmBinary);
        }
        if (readBinary) {
          return readBinary(file);
        } else {
          throw "both async and sync fetching of the wasm failed";
        }
      } catch (err2) {
        abort(err2);
      }
    }
    function getBinaryPromise() {
      if (!wasmBinary && (ENVIRONMENT_IS_WEB || ENVIRONMENT_IS_WORKER)) {
        if (typeof fetch == "function" && !isFileURI(wasmBinaryFile)) {
          return fetch(wasmBinaryFile, { credentials: "same-origin" }).then(function(response) {
            if (!response["ok"]) {
              throw "failed to load wasm binary file at '" + wasmBinaryFile + "'";
            }
            return response["arrayBuffer"]();
          }).catch(function() {
            return getBinary(wasmBinaryFile);
          });
        } else {
          if (readAsync) {
            return new Promise(function(resolve, reject) {
              readAsync(wasmBinaryFile, function(response) {
                resolve(new Uint8Array(response));
              }, reject);
            });
          }
        }
      }
      return Promise.resolve().then(function() {
        return getBinary(wasmBinaryFile);
      });
    }
    function createWasm() {
      var info = { a: asmLibraryArg };
      function receiveInstance(instance, module) {
        var exports2 = instance.exports;
        Module2["asm"] = exports2;
        wasmMemory = Module2["asm"]["oa"];
        updateGlobalBufferAndViews(wasmMemory.buffer);
        wasmTable = Module2["asm"]["va"];
        addOnInit(Module2["asm"]["pa"]);
        removeRunDependency("wasm-instantiate");
      }
      addRunDependency("wasm-instantiate");
      function receiveInstantiationResult(result) {
        receiveInstance(result["instance"]);
      }
      function instantiateArrayBuffer(receiver) {
        return getBinaryPromise().then(function(binary) {
          return WebAssembly.instantiate(binary, info);
        }).then(function(instance) {
          return instance;
        }).then(receiver, function(reason) {
          err("failed to asynchronously prepare wasm: " + reason);
          abort(reason);
        });
      }
      function instantiateAsync() {
        if (!wasmBinary && typeof WebAssembly.instantiateStreaming == "function" && !isDataURI(wasmBinaryFile) && !isFileURI(wasmBinaryFile) && !ENVIRONMENT_IS_NODE && typeof fetch == "function") {
          return fetch(wasmBinaryFile, { credentials: "same-origin" }).then(function(response) {
            var result = WebAssembly.instantiateStreaming(response, info);
            return result.then(receiveInstantiationResult, function(reason) {
              err("wasm streaming compile failed: " + reason);
              err("falling back to ArrayBuffer instantiation");
              return instantiateArrayBuffer(receiveInstantiationResult);
            });
          });
        } else {
          return instantiateArrayBuffer(receiveInstantiationResult);
        }
      }
      if (Module2["instantiateWasm"]) {
        try {
          var exports = Module2["instantiateWasm"](info, receiveInstance);
          return exports;
        } catch (e) {
          err("Module.instantiateWasm callback failed with error: " + e);
          return false;
        }
      }
      instantiateAsync().catch(readyPromiseReject);
      return {};
    }
    var tempDouble;
    var tempI64;
    function OSD_MemInfo_getModuleHeapLength() {
      return Module2.HEAP8.length;
    }
    function callRuntimeCallbacks(callbacks) {
      while (callbacks.length > 0) {
        var callback = callbacks.shift();
        if (typeof callback == "function") {
          callback(Module2);
          continue;
        }
        var func = callback.func;
        if (typeof func == "number") {
          if (callback.arg === undefined) {
            getWasmTableEntry(func)();
          } else {
            getWasmTableEntry(func)(callback.arg);
          }
        } else {
          func(callback.arg === undefined ? null : callback.arg);
        }
      }
    }
    var wasmTableMirror = [];
    function getWasmTableEntry(funcPtr) {
      var func = wasmTableMirror[funcPtr];
      if (!func) {
        if (funcPtr >= wasmTableMirror.length)
          wasmTableMirror.length = funcPtr + 1;
        wasmTableMirror[funcPtr] = func = wasmTable.get(funcPtr);
      }
      return func;
    }
    function ___assert_fail(condition, filename, line, func) {
      abort("Assertion failed: " + UTF8ToString(condition) + ", at: " + [filename ? UTF8ToString(filename) : "unknown filename", line, func ? UTF8ToString(func) : "unknown function"]);
    }
    function ___cxa_allocate_exception(size) {
      return _malloc(size + 24) + 24;
    }
    function ExceptionInfo(excPtr) {
      this.excPtr = excPtr;
      this.ptr = excPtr - 24;
      this.set_type = function(type) {
        HEAPU32[this.ptr + 4 >> 2] = type;
      };
      this.get_type = function() {
        return HEAPU32[this.ptr + 4 >> 2];
      };
      this.set_destructor = function(destructor) {
        HEAPU32[this.ptr + 8 >> 2] = destructor;
      };
      this.get_destructor = function() {
        return HEAPU32[this.ptr + 8 >> 2];
      };
      this.set_refcount = function(refcount) {
        HEAP32[this.ptr >> 2] = refcount;
      };
      this.set_caught = function(caught) {
        caught = caught ? 1 : 0;
        HEAP8[this.ptr + 12 >> 0] = caught;
      };
      this.get_caught = function() {
        return HEAP8[this.ptr + 12 >> 0] != 0;
      };
      this.set_rethrown = function(rethrown) {
        rethrown = rethrown ? 1 : 0;
        HEAP8[this.ptr + 13 >> 0] = rethrown;
      };
      this.get_rethrown = function() {
        return HEAP8[this.ptr + 13 >> 0] != 0;
      };
      this.init = function(type, destructor) {
        this.set_adjusted_ptr(0);
        this.set_type(type);
        this.set_destructor(destructor);
        this.set_refcount(0);
        this.set_caught(false);
        this.set_rethrown(false);
      };
      this.add_ref = function() {
        var value = HEAP32[this.ptr >> 2];
        HEAP32[this.ptr >> 2] = value + 1;
      };
      this.release_ref = function() {
        var prev = HEAP32[this.ptr >> 2];
        HEAP32[this.ptr >> 2] = prev - 1;
        return prev === 1;
      };
      this.set_adjusted_ptr = function(adjustedPtr) {
        HEAPU32[this.ptr + 16 >> 2] = adjustedPtr;
      };
      this.get_adjusted_ptr = function() {
        return HEAPU32[this.ptr + 16 >> 2];
      };
      this.get_exception_ptr = function() {
        var isPointer = ___cxa_is_pointer_type(this.get_type());
        if (isPointer) {
          return HEAPU32[this.excPtr >> 2];
        }
        var adjusted = this.get_adjusted_ptr();
        if (adjusted !== 0)
          return adjusted;
        return this.excPtr;
      };
    }
    var exceptionLast = 0;
    var uncaughtExceptionCount = 0;
    function ___cxa_throw(ptr, type, destructor) {
      var info = new ExceptionInfo(ptr);
      info.init(type, destructor);
      exceptionLast = ptr;
      uncaughtExceptionCount++;
      throw ptr;
    }
    var PATH = { isAbs: (path) => path.charAt(0) === "/", splitPath: (filename) => {
      var splitPathRe = /^(\/?|)([\s\S]*?)((?:\.{1,2}|[^\/]+?|)(\.[^.\/]*|))(?:[\/]*)$/;
      return splitPathRe.exec(filename).slice(1);
    }, normalizeArray: (parts, allowAboveRoot) => {
      var up = 0;
      for (var i2 = parts.length - 1;i2 >= 0; i2--) {
        var last = parts[i2];
        if (last === ".") {
          parts.splice(i2, 1);
        } else if (last === "..") {
          parts.splice(i2, 1);
          up++;
        } else if (up) {
          parts.splice(i2, 1);
          up--;
        }
      }
      if (allowAboveRoot) {
        for (;up; up--) {
          parts.unshift("..");
        }
      }
      return parts;
    }, normalize: (path) => {
      var isAbsolute = PATH.isAbs(path), trailingSlash = path.substr(-1) === "/";
      path = PATH.normalizeArray(path.split("/").filter((p) => !!p), !isAbsolute).join("/");
      if (!path && !isAbsolute) {
        path = ".";
      }
      if (path && trailingSlash) {
        path += "/";
      }
      return (isAbsolute ? "/" : "") + path;
    }, dirname: (path) => {
      var result = PATH.splitPath(path), root = result[0], dir = result[1];
      if (!root && !dir) {
        return ".";
      }
      if (dir) {
        dir = dir.substr(0, dir.length - 1);
      }
      return root + dir;
    }, basename: (path) => {
      if (path === "/")
        return "/";
      path = PATH.normalize(path);
      path = path.replace(/\/$/, "");
      var lastSlash = path.lastIndexOf("/");
      if (lastSlash === -1)
        return path;
      return path.substr(lastSlash + 1);
    }, join: function() {
      var paths = Array.prototype.slice.call(arguments, 0);
      return PATH.normalize(paths.join("/"));
    }, join2: (l, r) => {
      return PATH.normalize(l + "/" + r);
    } };
    function getRandomDevice() {
      if (typeof crypto == "object" && typeof crypto["getRandomValues"] == "function") {
        var randomBuffer = new Uint8Array(1);
        return function() {
          crypto.getRandomValues(randomBuffer);
          return randomBuffer[0];
        };
      } else if (ENVIRONMENT_IS_NODE) {
        try {
          var crypto_module = __require("crypto");
          return function() {
            return crypto_module["randomBytes"](1)[0];
          };
        } catch (e) {}
      }
      return function() {
        abort("randomDevice");
      };
    }
    var PATH_FS = { resolve: function() {
      var resolvedPath = "", resolvedAbsolute = false;
      for (var i2 = arguments.length - 1;i2 >= -1 && !resolvedAbsolute; i2--) {
        var path = i2 >= 0 ? arguments[i2] : FS.cwd();
        if (typeof path != "string") {
          throw new TypeError("Arguments to path.resolve must be strings");
        } else if (!path) {
          return "";
        }
        resolvedPath = path + "/" + resolvedPath;
        resolvedAbsolute = PATH.isAbs(path);
      }
      resolvedPath = PATH.normalizeArray(resolvedPath.split("/").filter((p) => !!p), !resolvedAbsolute).join("/");
      return (resolvedAbsolute ? "/" : "") + resolvedPath || ".";
    }, relative: (from, to) => {
      from = PATH_FS.resolve(from).substr(1);
      to = PATH_FS.resolve(to).substr(1);
      function trim(arr) {
        var start = 0;
        for (;start < arr.length; start++) {
          if (arr[start] !== "")
            break;
        }
        var end = arr.length - 1;
        for (;end >= 0; end--) {
          if (arr[end] !== "")
            break;
        }
        if (start > end)
          return [];
        return arr.slice(start, end - start + 1);
      }
      var fromParts = trim(from.split("/"));
      var toParts = trim(to.split("/"));
      var length = Math.min(fromParts.length, toParts.length);
      var samePartsLength = length;
      for (var i2 = 0;i2 < length; i2++) {
        if (fromParts[i2] !== toParts[i2]) {
          samePartsLength = i2;
          break;
        }
      }
      var outputParts = [];
      for (var i2 = samePartsLength;i2 < fromParts.length; i2++) {
        outputParts.push("..");
      }
      outputParts = outputParts.concat(toParts.slice(samePartsLength));
      return outputParts.join("/");
    } };
    var TTY = { ttys: [], init: function() {}, shutdown: function() {}, register: function(dev, ops) {
      TTY.ttys[dev] = { input: [], output: [], ops };
      FS.registerDevice(dev, TTY.stream_ops);
    }, stream_ops: { open: function(stream) {
      var tty = TTY.ttys[stream.node.rdev];
      if (!tty) {
        throw new FS.ErrnoError(43);
      }
      stream.tty = tty;
      stream.seekable = false;
    }, close: function(stream) {
      stream.tty.ops.flush(stream.tty);
    }, flush: function(stream) {
      stream.tty.ops.flush(stream.tty);
    }, read: function(stream, buffer2, offset, length, pos) {
      if (!stream.tty || !stream.tty.ops.get_char) {
        throw new FS.ErrnoError(60);
      }
      var bytesRead = 0;
      for (var i2 = 0;i2 < length; i2++) {
        var result;
        try {
          result = stream.tty.ops.get_char(stream.tty);
        } catch (e) {
          throw new FS.ErrnoError(29);
        }
        if (result === undefined && bytesRead === 0) {
          throw new FS.ErrnoError(6);
        }
        if (result === null || result === undefined)
          break;
        bytesRead++;
        buffer2[offset + i2] = result;
      }
      if (bytesRead) {
        stream.node.timestamp = Date.now();
      }
      return bytesRead;
    }, write: function(stream, buffer2, offset, length, pos) {
      if (!stream.tty || !stream.tty.ops.put_char) {
        throw new FS.ErrnoError(60);
      }
      try {
        for (var i2 = 0;i2 < length; i2++) {
          stream.tty.ops.put_char(stream.tty, buffer2[offset + i2]);
        }
      } catch (e) {
        throw new FS.ErrnoError(29);
      }
      if (length) {
        stream.node.timestamp = Date.now();
      }
      return i2;
    } }, default_tty_ops: { get_char: function(tty) {
      if (!tty.input.length) {
        var result = null;
        if (ENVIRONMENT_IS_NODE) {
          var BUFSIZE = 256;
          var buf = Buffer.alloc(BUFSIZE);
          var bytesRead = 0;
          try {
            bytesRead = fs.readSync(process.stdin.fd, buf, 0, BUFSIZE, -1);
          } catch (e) {
            if (e.toString().includes("EOF"))
              bytesRead = 0;
            else
              throw e;
          }
          if (bytesRead > 0) {
            result = buf.slice(0, bytesRead).toString("utf-8");
          } else {
            result = null;
          }
        } else if (typeof window != "undefined" && typeof window.prompt == "function") {
          result = window.prompt("Input: ");
          if (result !== null) {
            result += `
`;
          }
        } else if (typeof readline == "function") {
          result = readline();
          if (result !== null) {
            result += `
`;
          }
        }
        if (!result) {
          return null;
        }
        tty.input = intArrayFromString(result, true);
      }
      return tty.input.shift();
    }, put_char: function(tty, val) {
      if (val === null || val === 10) {
        out(UTF8ArrayToString(tty.output, 0));
        tty.output = [];
      } else {
        if (val != 0)
          tty.output.push(val);
      }
    }, flush: function(tty) {
      if (tty.output && tty.output.length > 0) {
        out(UTF8ArrayToString(tty.output, 0));
        tty.output = [];
      }
    } }, default_tty1_ops: { put_char: function(tty, val) {
      if (val === null || val === 10) {
        err(UTF8ArrayToString(tty.output, 0));
        tty.output = [];
      } else {
        if (val != 0)
          tty.output.push(val);
      }
    }, flush: function(tty) {
      if (tty.output && tty.output.length > 0) {
        err(UTF8ArrayToString(tty.output, 0));
        tty.output = [];
      }
    } } };
    function zeroMemory(address, size) {
      HEAPU8.fill(0, address, address + size);
    }
    function alignMemory(size, alignment) {
      return Math.ceil(size / alignment) * alignment;
    }
    function mmapAlloc(size) {
      size = alignMemory(size, 65536);
      var ptr = _emscripten_builtin_memalign(65536, size);
      if (!ptr)
        return 0;
      zeroMemory(ptr, size);
      return ptr;
    }
    var MEMFS = { ops_table: null, mount: function(mount) {
      return MEMFS.createNode(null, "/", 16384 | 511, 0);
    }, createNode: function(parent, name, mode, dev) {
      if (FS.isBlkdev(mode) || FS.isFIFO(mode)) {
        throw new FS.ErrnoError(63);
      }
      if (!MEMFS.ops_table) {
        MEMFS.ops_table = { dir: { node: { getattr: MEMFS.node_ops.getattr, setattr: MEMFS.node_ops.setattr, lookup: MEMFS.node_ops.lookup, mknod: MEMFS.node_ops.mknod, rename: MEMFS.node_ops.rename, unlink: MEMFS.node_ops.unlink, rmdir: MEMFS.node_ops.rmdir, readdir: MEMFS.node_ops.readdir, symlink: MEMFS.node_ops.symlink }, stream: { llseek: MEMFS.stream_ops.llseek } }, file: { node: { getattr: MEMFS.node_ops.getattr, setattr: MEMFS.node_ops.setattr }, stream: { llseek: MEMFS.stream_ops.llseek, read: MEMFS.stream_ops.read, write: MEMFS.stream_ops.write, allocate: MEMFS.stream_ops.allocate, mmap: MEMFS.stream_ops.mmap, msync: MEMFS.stream_ops.msync } }, link: { node: { getattr: MEMFS.node_ops.getattr, setattr: MEMFS.node_ops.setattr, readlink: MEMFS.node_ops.readlink }, stream: {} }, chrdev: { node: { getattr: MEMFS.node_ops.getattr, setattr: MEMFS.node_ops.setattr }, stream: FS.chrdev_stream_ops } };
      }
      var node = FS.createNode(parent, name, mode, dev);
      if (FS.isDir(node.mode)) {
        node.node_ops = MEMFS.ops_table.dir.node;
        node.stream_ops = MEMFS.ops_table.dir.stream;
        node.contents = {};
      } else if (FS.isFile(node.mode)) {
        node.node_ops = MEMFS.ops_table.file.node;
        node.stream_ops = MEMFS.ops_table.file.stream;
        node.usedBytes = 0;
        node.contents = null;
      } else if (FS.isLink(node.mode)) {
        node.node_ops = MEMFS.ops_table.link.node;
        node.stream_ops = MEMFS.ops_table.link.stream;
      } else if (FS.isChrdev(node.mode)) {
        node.node_ops = MEMFS.ops_table.chrdev.node;
        node.stream_ops = MEMFS.ops_table.chrdev.stream;
      }
      node.timestamp = Date.now();
      if (parent) {
        parent.contents[name] = node;
        parent.timestamp = node.timestamp;
      }
      return node;
    }, getFileDataAsTypedArray: function(node) {
      if (!node.contents)
        return new Uint8Array(0);
      if (node.contents.subarray)
        return node.contents.subarray(0, node.usedBytes);
      return new Uint8Array(node.contents);
    }, expandFileStorage: function(node, newCapacity) {
      var prevCapacity = node.contents ? node.contents.length : 0;
      if (prevCapacity >= newCapacity)
        return;
      var CAPACITY_DOUBLING_MAX = 1024 * 1024;
      newCapacity = Math.max(newCapacity, prevCapacity * (prevCapacity < CAPACITY_DOUBLING_MAX ? 2 : 1.125) >>> 0);
      if (prevCapacity != 0)
        newCapacity = Math.max(newCapacity, 256);
      var oldContents = node.contents;
      node.contents = new Uint8Array(newCapacity);
      if (node.usedBytes > 0)
        node.contents.set(oldContents.subarray(0, node.usedBytes), 0);
    }, resizeFileStorage: function(node, newSize) {
      if (node.usedBytes == newSize)
        return;
      if (newSize == 0) {
        node.contents = null;
        node.usedBytes = 0;
      } else {
        var oldContents = node.contents;
        node.contents = new Uint8Array(newSize);
        if (oldContents) {
          node.contents.set(oldContents.subarray(0, Math.min(newSize, node.usedBytes)));
        }
        node.usedBytes = newSize;
      }
    }, node_ops: { getattr: function(node) {
      var attr = {};
      attr.dev = FS.isChrdev(node.mode) ? node.id : 1;
      attr.ino = node.id;
      attr.mode = node.mode;
      attr.nlink = 1;
      attr.uid = 0;
      attr.gid = 0;
      attr.rdev = node.rdev;
      if (FS.isDir(node.mode)) {
        attr.size = 4096;
      } else if (FS.isFile(node.mode)) {
        attr.size = node.usedBytes;
      } else if (FS.isLink(node.mode)) {
        attr.size = node.link.length;
      } else {
        attr.size = 0;
      }
      attr.atime = new Date(node.timestamp);
      attr.mtime = new Date(node.timestamp);
      attr.ctime = new Date(node.timestamp);
      attr.blksize = 4096;
      attr.blocks = Math.ceil(attr.size / attr.blksize);
      return attr;
    }, setattr: function(node, attr) {
      if (attr.mode !== undefined) {
        node.mode = attr.mode;
      }
      if (attr.timestamp !== undefined) {
        node.timestamp = attr.timestamp;
      }
      if (attr.size !== undefined) {
        MEMFS.resizeFileStorage(node, attr.size);
      }
    }, lookup: function(parent, name) {
      throw FS.genericErrors[44];
    }, mknod: function(parent, name, mode, dev) {
      return MEMFS.createNode(parent, name, mode, dev);
    }, rename: function(old_node, new_dir, new_name) {
      if (FS.isDir(old_node.mode)) {
        var new_node;
        try {
          new_node = FS.lookupNode(new_dir, new_name);
        } catch (e) {}
        if (new_node) {
          for (var i2 in new_node.contents) {
            throw new FS.ErrnoError(55);
          }
        }
      }
      delete old_node.parent.contents[old_node.name];
      old_node.parent.timestamp = Date.now();
      old_node.name = new_name;
      new_dir.contents[new_name] = old_node;
      new_dir.timestamp = old_node.parent.timestamp;
      old_node.parent = new_dir;
    }, unlink: function(parent, name) {
      delete parent.contents[name];
      parent.timestamp = Date.now();
    }, rmdir: function(parent, name) {
      var node = FS.lookupNode(parent, name);
      for (var i2 in node.contents) {
        throw new FS.ErrnoError(55);
      }
      delete parent.contents[name];
      parent.timestamp = Date.now();
    }, readdir: function(node) {
      var entries = [".", ".."];
      for (var key in node.contents) {
        if (!node.contents.hasOwnProperty(key)) {
          continue;
        }
        entries.push(key);
      }
      return entries;
    }, symlink: function(parent, newname, oldpath) {
      var node = MEMFS.createNode(parent, newname, 511 | 40960, 0);
      node.link = oldpath;
      return node;
    }, readlink: function(node) {
      if (!FS.isLink(node.mode)) {
        throw new FS.ErrnoError(28);
      }
      return node.link;
    } }, stream_ops: { read: function(stream, buffer2, offset, length, position) {
      var contents = stream.node.contents;
      if (position >= stream.node.usedBytes)
        return 0;
      var size = Math.min(stream.node.usedBytes - position, length);
      if (size > 8 && contents.subarray) {
        buffer2.set(contents.subarray(position, position + size), offset);
      } else {
        for (var i2 = 0;i2 < size; i2++)
          buffer2[offset + i2] = contents[position + i2];
      }
      return size;
    }, write: function(stream, buffer2, offset, length, position, canOwn) {
      if (buffer2.buffer === HEAP8.buffer) {
        canOwn = false;
      }
      if (!length)
        return 0;
      var node = stream.node;
      node.timestamp = Date.now();
      if (buffer2.subarray && (!node.contents || node.contents.subarray)) {
        if (canOwn) {
          node.contents = buffer2.subarray(offset, offset + length);
          node.usedBytes = length;
          return length;
        } else if (node.usedBytes === 0 && position === 0) {
          node.contents = buffer2.slice(offset, offset + length);
          node.usedBytes = length;
          return length;
        } else if (position + length <= node.usedBytes) {
          node.contents.set(buffer2.subarray(offset, offset + length), position);
          return length;
        }
      }
      MEMFS.expandFileStorage(node, position + length);
      if (node.contents.subarray && buffer2.subarray) {
        node.contents.set(buffer2.subarray(offset, offset + length), position);
      } else {
        for (var i2 = 0;i2 < length; i2++) {
          node.contents[position + i2] = buffer2[offset + i2];
        }
      }
      node.usedBytes = Math.max(node.usedBytes, position + length);
      return length;
    }, llseek: function(stream, offset, whence) {
      var position = offset;
      if (whence === 1) {
        position += stream.position;
      } else if (whence === 2) {
        if (FS.isFile(stream.node.mode)) {
          position += stream.node.usedBytes;
        }
      }
      if (position < 0) {
        throw new FS.ErrnoError(28);
      }
      return position;
    }, allocate: function(stream, offset, length) {
      MEMFS.expandFileStorage(stream.node, offset + length);
      stream.node.usedBytes = Math.max(stream.node.usedBytes, offset + length);
    }, mmap: function(stream, length, position, prot, flags) {
      if (!FS.isFile(stream.node.mode)) {
        throw new FS.ErrnoError(43);
      }
      var ptr;
      var allocated;
      var contents = stream.node.contents;
      if (!(flags & 2) && contents.buffer === buffer) {
        allocated = false;
        ptr = contents.byteOffset;
      } else {
        if (position > 0 || position + length < contents.length) {
          if (contents.subarray) {
            contents = contents.subarray(position, position + length);
          } else {
            contents = Array.prototype.slice.call(contents, position, position + length);
          }
        }
        allocated = true;
        ptr = mmapAlloc(length);
        if (!ptr) {
          throw new FS.ErrnoError(48);
        }
        HEAP8.set(contents, ptr);
      }
      return { ptr, allocated };
    }, msync: function(stream, buffer2, offset, length, mmapFlags) {
      if (!FS.isFile(stream.node.mode)) {
        throw new FS.ErrnoError(43);
      }
      if (mmapFlags & 2) {
        return 0;
      }
      var bytesWritten = MEMFS.stream_ops.write(stream, buffer2, 0, length, offset, false);
      return 0;
    } } };
    function asyncLoad(url, onload, onerror, noRunDep) {
      var dep = !noRunDep ? getUniqueRunDependency("al " + url) : "";
      readAsync(url, function(arrayBuffer) {
        assert(arrayBuffer, 'Loading data file "' + url + '" failed (no arrayBuffer).');
        onload(new Uint8Array(arrayBuffer));
        if (dep)
          removeRunDependency(dep);
      }, function(event) {
        if (onerror) {
          onerror();
        } else {
          throw 'Loading data file "' + url + '" failed.';
        }
      });
      if (dep)
        addRunDependency(dep);
    }
    var FS = { root: null, mounts: [], devices: {}, streams: [], nextInode: 1, nameTable: null, currentPath: "/", initialized: false, ignorePermissions: true, ErrnoError: null, genericErrors: {}, filesystems: null, syncFSRequests: 0, lookupPath: (path, opts = {}) => {
      path = PATH_FS.resolve(FS.cwd(), path);
      if (!path)
        return { path: "", node: null };
      var defaults = { follow_mount: true, recurse_count: 0 };
      opts = Object.assign(defaults, opts);
      if (opts.recurse_count > 8) {
        throw new FS.ErrnoError(32);
      }
      var parts = PATH.normalizeArray(path.split("/").filter((p) => !!p), false);
      var current = FS.root;
      var current_path = "/";
      for (var i2 = 0;i2 < parts.length; i2++) {
        var islast = i2 === parts.length - 1;
        if (islast && opts.parent) {
          break;
        }
        current = FS.lookupNode(current, parts[i2]);
        current_path = PATH.join2(current_path, parts[i2]);
        if (FS.isMountpoint(current)) {
          if (!islast || islast && opts.follow_mount) {
            current = current.mounted.root;
          }
        }
        if (!islast || opts.follow) {
          var count = 0;
          while (FS.isLink(current.mode)) {
            var link = FS.readlink(current_path);
            current_path = PATH_FS.resolve(PATH.dirname(current_path), link);
            var lookup = FS.lookupPath(current_path, { recurse_count: opts.recurse_count + 1 });
            current = lookup.node;
            if (count++ > 40) {
              throw new FS.ErrnoError(32);
            }
          }
        }
      }
      return { path: current_path, node: current };
    }, getPath: (node) => {
      var path;
      while (true) {
        if (FS.isRoot(node)) {
          var mount = node.mount.mountpoint;
          if (!path)
            return mount;
          return mount[mount.length - 1] !== "/" ? mount + "/" + path : mount + path;
        }
        path = path ? node.name + "/" + path : node.name;
        node = node.parent;
      }
    }, hashName: (parentid, name) => {
      var hash = 0;
      for (var i2 = 0;i2 < name.length; i2++) {
        hash = (hash << 5) - hash + name.charCodeAt(i2) | 0;
      }
      return (parentid + hash >>> 0) % FS.nameTable.length;
    }, hashAddNode: (node) => {
      var hash = FS.hashName(node.parent.id, node.name);
      node.name_next = FS.nameTable[hash];
      FS.nameTable[hash] = node;
    }, hashRemoveNode: (node) => {
      var hash = FS.hashName(node.parent.id, node.name);
      if (FS.nameTable[hash] === node) {
        FS.nameTable[hash] = node.name_next;
      } else {
        var current = FS.nameTable[hash];
        while (current) {
          if (current.name_next === node) {
            current.name_next = node.name_next;
            break;
          }
          current = current.name_next;
        }
      }
    }, lookupNode: (parent, name) => {
      var errCode = FS.mayLookup(parent);
      if (errCode) {
        throw new FS.ErrnoError(errCode, parent);
      }
      var hash = FS.hashName(parent.id, name);
      for (var node = FS.nameTable[hash];node; node = node.name_next) {
        var nodeName = node.name;
        if (node.parent.id === parent.id && nodeName === name) {
          return node;
        }
      }
      return FS.lookup(parent, name);
    }, createNode: (parent, name, mode, rdev) => {
      var node = new FS.FSNode(parent, name, mode, rdev);
      FS.hashAddNode(node);
      return node;
    }, destroyNode: (node) => {
      FS.hashRemoveNode(node);
    }, isRoot: (node) => {
      return node === node.parent;
    }, isMountpoint: (node) => {
      return !!node.mounted;
    }, isFile: (mode) => {
      return (mode & 61440) === 32768;
    }, isDir: (mode) => {
      return (mode & 61440) === 16384;
    }, isLink: (mode) => {
      return (mode & 61440) === 40960;
    }, isChrdev: (mode) => {
      return (mode & 61440) === 8192;
    }, isBlkdev: (mode) => {
      return (mode & 61440) === 24576;
    }, isFIFO: (mode) => {
      return (mode & 61440) === 4096;
    }, isSocket: (mode) => {
      return (mode & 49152) === 49152;
    }, flagModes: { r: 0, "r+": 2, w: 577, "w+": 578, a: 1089, "a+": 1090 }, modeStringToFlags: (str) => {
      var flags = FS.flagModes[str];
      if (typeof flags == "undefined") {
        throw new Error("Unknown file open mode: " + str);
      }
      return flags;
    }, flagsToPermissionString: (flag) => {
      var perms = ["r", "w", "rw"][flag & 3];
      if (flag & 512) {
        perms += "w";
      }
      return perms;
    }, nodePermissions: (node, perms) => {
      if (FS.ignorePermissions) {
        return 0;
      }
      if (perms.includes("r") && !(node.mode & 292)) {
        return 2;
      } else if (perms.includes("w") && !(node.mode & 146)) {
        return 2;
      } else if (perms.includes("x") && !(node.mode & 73)) {
        return 2;
      }
      return 0;
    }, mayLookup: (dir) => {
      var errCode = FS.nodePermissions(dir, "x");
      if (errCode)
        return errCode;
      if (!dir.node_ops.lookup)
        return 2;
      return 0;
    }, mayCreate: (dir, name) => {
      try {
        var node = FS.lookupNode(dir, name);
        return 20;
      } catch (e) {}
      return FS.nodePermissions(dir, "wx");
    }, mayDelete: (dir, name, isdir) => {
      var node;
      try {
        node = FS.lookupNode(dir, name);
      } catch (e) {
        return e.errno;
      }
      var errCode = FS.nodePermissions(dir, "wx");
      if (errCode) {
        return errCode;
      }
      if (isdir) {
        if (!FS.isDir(node.mode)) {
          return 54;
        }
        if (FS.isRoot(node) || FS.getPath(node) === FS.cwd()) {
          return 10;
        }
      } else {
        if (FS.isDir(node.mode)) {
          return 31;
        }
      }
      return 0;
    }, mayOpen: (node, flags) => {
      if (!node) {
        return 44;
      }
      if (FS.isLink(node.mode)) {
        return 32;
      } else if (FS.isDir(node.mode)) {
        if (FS.flagsToPermissionString(flags) !== "r" || flags & 512) {
          return 31;
        }
      }
      return FS.nodePermissions(node, FS.flagsToPermissionString(flags));
    }, MAX_OPEN_FDS: 4096, nextfd: (fd_start = 0, fd_end = FS.MAX_OPEN_FDS) => {
      for (var fd = fd_start;fd <= fd_end; fd++) {
        if (!FS.streams[fd]) {
          return fd;
        }
      }
      throw new FS.ErrnoError(33);
    }, getStream: (fd) => FS.streams[fd], createStream: (stream, fd_start, fd_end) => {
      if (!FS.FSStream) {
        FS.FSStream = function() {
          this.shared = {};
        };
        FS.FSStream.prototype = {};
        Object.defineProperties(FS.FSStream.prototype, { object: { get: function() {
          return this.node;
        }, set: function(val) {
          this.node = val;
        } }, isRead: { get: function() {
          return (this.flags & 2097155) !== 1;
        } }, isWrite: { get: function() {
          return (this.flags & 2097155) !== 0;
        } }, isAppend: { get: function() {
          return this.flags & 1024;
        } }, flags: { get: function() {
          return this.shared.flags;
        }, set: function(val) {
          this.shared.flags = val;
        } }, position: { get: function() {
          return this.shared.position;
        }, set: function(val) {
          this.shared.position = val;
        } } });
      }
      stream = Object.assign(new FS.FSStream, stream);
      var fd = FS.nextfd(fd_start, fd_end);
      stream.fd = fd;
      FS.streams[fd] = stream;
      return stream;
    }, closeStream: (fd) => {
      FS.streams[fd] = null;
    }, chrdev_stream_ops: { open: (stream) => {
      var device = FS.getDevice(stream.node.rdev);
      stream.stream_ops = device.stream_ops;
      if (stream.stream_ops.open) {
        stream.stream_ops.open(stream);
      }
    }, llseek: () => {
      throw new FS.ErrnoError(70);
    } }, major: (dev) => dev >> 8, minor: (dev) => dev & 255, makedev: (ma, mi) => ma << 8 | mi, registerDevice: (dev, ops) => {
      FS.devices[dev] = { stream_ops: ops };
    }, getDevice: (dev) => FS.devices[dev], getMounts: (mount) => {
      var mounts = [];
      var check = [mount];
      while (check.length) {
        var m = check.pop();
        mounts.push(m);
        check.push.apply(check, m.mounts);
      }
      return mounts;
    }, syncfs: (populate, callback) => {
      if (typeof populate == "function") {
        callback = populate;
        populate = false;
      }
      FS.syncFSRequests++;
      if (FS.syncFSRequests > 1) {
        err("warning: " + FS.syncFSRequests + " FS.syncfs operations in flight at once, probably just doing extra work");
      }
      var mounts = FS.getMounts(FS.root.mount);
      var completed = 0;
      function doCallback(errCode) {
        FS.syncFSRequests--;
        return callback(errCode);
      }
      function done(errCode) {
        if (errCode) {
          if (!done.errored) {
            done.errored = true;
            return doCallback(errCode);
          }
          return;
        }
        if (++completed >= mounts.length) {
          doCallback(null);
        }
      }
      mounts.forEach((mount) => {
        if (!mount.type.syncfs) {
          return done(null);
        }
        mount.type.syncfs(mount, populate, done);
      });
    }, mount: (type, opts, mountpoint) => {
      var root = mountpoint === "/";
      var pseudo = !mountpoint;
      var node;
      if (root && FS.root) {
        throw new FS.ErrnoError(10);
      } else if (!root && !pseudo) {
        var lookup = FS.lookupPath(mountpoint, { follow_mount: false });
        mountpoint = lookup.path;
        node = lookup.node;
        if (FS.isMountpoint(node)) {
          throw new FS.ErrnoError(10);
        }
        if (!FS.isDir(node.mode)) {
          throw new FS.ErrnoError(54);
        }
      }
      var mount = { type, opts, mountpoint, mounts: [] };
      var mountRoot = type.mount(mount);
      mountRoot.mount = mount;
      mount.root = mountRoot;
      if (root) {
        FS.root = mountRoot;
      } else if (node) {
        node.mounted = mount;
        if (node.mount) {
          node.mount.mounts.push(mount);
        }
      }
      return mountRoot;
    }, unmount: (mountpoint) => {
      var lookup = FS.lookupPath(mountpoint, { follow_mount: false });
      if (!FS.isMountpoint(lookup.node)) {
        throw new FS.ErrnoError(28);
      }
      var node = lookup.node;
      var mount = node.mounted;
      var mounts = FS.getMounts(mount);
      Object.keys(FS.nameTable).forEach((hash) => {
        var current = FS.nameTable[hash];
        while (current) {
          var next = current.name_next;
          if (mounts.includes(current.mount)) {
            FS.destroyNode(current);
          }
          current = next;
        }
      });
      node.mounted = null;
      var idx = node.mount.mounts.indexOf(mount);
      node.mount.mounts.splice(idx, 1);
    }, lookup: (parent, name) => {
      return parent.node_ops.lookup(parent, name);
    }, mknod: (path, mode, dev) => {
      var lookup = FS.lookupPath(path, { parent: true });
      var parent = lookup.node;
      var name = PATH.basename(path);
      if (!name || name === "." || name === "..") {
        throw new FS.ErrnoError(28);
      }
      var errCode = FS.mayCreate(parent, name);
      if (errCode) {
        throw new FS.ErrnoError(errCode);
      }
      if (!parent.node_ops.mknod) {
        throw new FS.ErrnoError(63);
      }
      return parent.node_ops.mknod(parent, name, mode, dev);
    }, create: (path, mode) => {
      mode = mode !== undefined ? mode : 438;
      mode &= 4095;
      mode |= 32768;
      return FS.mknod(path, mode, 0);
    }, mkdir: (path, mode) => {
      mode = mode !== undefined ? mode : 511;
      mode &= 511 | 512;
      mode |= 16384;
      return FS.mknod(path, mode, 0);
    }, mkdirTree: (path, mode) => {
      var dirs = path.split("/");
      var d = "";
      for (var i2 = 0;i2 < dirs.length; ++i2) {
        if (!dirs[i2])
          continue;
        d += "/" + dirs[i2];
        try {
          FS.mkdir(d, mode);
        } catch (e) {
          if (e.errno != 20)
            throw e;
        }
      }
    }, mkdev: (path, mode, dev) => {
      if (typeof dev == "undefined") {
        dev = mode;
        mode = 438;
      }
      mode |= 8192;
      return FS.mknod(path, mode, dev);
    }, symlink: (oldpath, newpath) => {
      if (!PATH_FS.resolve(oldpath)) {
        throw new FS.ErrnoError(44);
      }
      var lookup = FS.lookupPath(newpath, { parent: true });
      var parent = lookup.node;
      if (!parent) {
        throw new FS.ErrnoError(44);
      }
      var newname = PATH.basename(newpath);
      var errCode = FS.mayCreate(parent, newname);
      if (errCode) {
        throw new FS.ErrnoError(errCode);
      }
      if (!parent.node_ops.symlink) {
        throw new FS.ErrnoError(63);
      }
      return parent.node_ops.symlink(parent, newname, oldpath);
    }, rename: (old_path, new_path) => {
      var old_dirname = PATH.dirname(old_path);
      var new_dirname = PATH.dirname(new_path);
      var old_name = PATH.basename(old_path);
      var new_name = PATH.basename(new_path);
      var lookup, old_dir, new_dir;
      lookup = FS.lookupPath(old_path, { parent: true });
      old_dir = lookup.node;
      lookup = FS.lookupPath(new_path, { parent: true });
      new_dir = lookup.node;
      if (!old_dir || !new_dir)
        throw new FS.ErrnoError(44);
      if (old_dir.mount !== new_dir.mount) {
        throw new FS.ErrnoError(75);
      }
      var old_node = FS.lookupNode(old_dir, old_name);
      var relative = PATH_FS.relative(old_path, new_dirname);
      if (relative.charAt(0) !== ".") {
        throw new FS.ErrnoError(28);
      }
      relative = PATH_FS.relative(new_path, old_dirname);
      if (relative.charAt(0) !== ".") {
        throw new FS.ErrnoError(55);
      }
      var new_node;
      try {
        new_node = FS.lookupNode(new_dir, new_name);
      } catch (e) {}
      if (old_node === new_node) {
        return;
      }
      var isdir = FS.isDir(old_node.mode);
      var errCode = FS.mayDelete(old_dir, old_name, isdir);
      if (errCode) {
        throw new FS.ErrnoError(errCode);
      }
      errCode = new_node ? FS.mayDelete(new_dir, new_name, isdir) : FS.mayCreate(new_dir, new_name);
      if (errCode) {
        throw new FS.ErrnoError(errCode);
      }
      if (!old_dir.node_ops.rename) {
        throw new FS.ErrnoError(63);
      }
      if (FS.isMountpoint(old_node) || new_node && FS.isMountpoint(new_node)) {
        throw new FS.ErrnoError(10);
      }
      if (new_dir !== old_dir) {
        errCode = FS.nodePermissions(old_dir, "w");
        if (errCode) {
          throw new FS.ErrnoError(errCode);
        }
      }
      FS.hashRemoveNode(old_node);
      try {
        old_dir.node_ops.rename(old_node, new_dir, new_name);
      } catch (e) {
        throw e;
      } finally {
        FS.hashAddNode(old_node);
      }
    }, rmdir: (path) => {
      var lookup = FS.lookupPath(path, { parent: true });
      var parent = lookup.node;
      var name = PATH.basename(path);
      var node = FS.lookupNode(parent, name);
      var errCode = FS.mayDelete(parent, name, true);
      if (errCode) {
        throw new FS.ErrnoError(errCode);
      }
      if (!parent.node_ops.rmdir) {
        throw new FS.ErrnoError(63);
      }
      if (FS.isMountpoint(node)) {
        throw new FS.ErrnoError(10);
      }
      parent.node_ops.rmdir(parent, name);
      FS.destroyNode(node);
    }, readdir: (path) => {
      var lookup = FS.lookupPath(path, { follow: true });
      var node = lookup.node;
      if (!node.node_ops.readdir) {
        throw new FS.ErrnoError(54);
      }
      return node.node_ops.readdir(node);
    }, unlink: (path) => {
      var lookup = FS.lookupPath(path, { parent: true });
      var parent = lookup.node;
      if (!parent) {
        throw new FS.ErrnoError(44);
      }
      var name = PATH.basename(path);
      var node = FS.lookupNode(parent, name);
      var errCode = FS.mayDelete(parent, name, false);
      if (errCode) {
        throw new FS.ErrnoError(errCode);
      }
      if (!parent.node_ops.unlink) {
        throw new FS.ErrnoError(63);
      }
      if (FS.isMountpoint(node)) {
        throw new FS.ErrnoError(10);
      }
      parent.node_ops.unlink(parent, name);
      FS.destroyNode(node);
    }, readlink: (path) => {
      var lookup = FS.lookupPath(path);
      var link = lookup.node;
      if (!link) {
        throw new FS.ErrnoError(44);
      }
      if (!link.node_ops.readlink) {
        throw new FS.ErrnoError(28);
      }
      return PATH_FS.resolve(FS.getPath(link.parent), link.node_ops.readlink(link));
    }, stat: (path, dontFollow) => {
      var lookup = FS.lookupPath(path, { follow: !dontFollow });
      var node = lookup.node;
      if (!node) {
        throw new FS.ErrnoError(44);
      }
      if (!node.node_ops.getattr) {
        throw new FS.ErrnoError(63);
      }
      return node.node_ops.getattr(node);
    }, lstat: (path) => {
      return FS.stat(path, true);
    }, chmod: (path, mode, dontFollow) => {
      var node;
      if (typeof path == "string") {
        var lookup = FS.lookupPath(path, { follow: !dontFollow });
        node = lookup.node;
      } else {
        node = path;
      }
      if (!node.node_ops.setattr) {
        throw new FS.ErrnoError(63);
      }
      node.node_ops.setattr(node, { mode: mode & 4095 | node.mode & ~4095, timestamp: Date.now() });
    }, lchmod: (path, mode) => {
      FS.chmod(path, mode, true);
    }, fchmod: (fd, mode) => {
      var stream = FS.getStream(fd);
      if (!stream) {
        throw new FS.ErrnoError(8);
      }
      FS.chmod(stream.node, mode);
    }, chown: (path, uid, gid, dontFollow) => {
      var node;
      if (typeof path == "string") {
        var lookup = FS.lookupPath(path, { follow: !dontFollow });
        node = lookup.node;
      } else {
        node = path;
      }
      if (!node.node_ops.setattr) {
        throw new FS.ErrnoError(63);
      }
      node.node_ops.setattr(node, { timestamp: Date.now() });
    }, lchown: (path, uid, gid) => {
      FS.chown(path, uid, gid, true);
    }, fchown: (fd, uid, gid) => {
      var stream = FS.getStream(fd);
      if (!stream) {
        throw new FS.ErrnoError(8);
      }
      FS.chown(stream.node, uid, gid);
    }, truncate: (path, len) => {
      if (len < 0) {
        throw new FS.ErrnoError(28);
      }
      var node;
      if (typeof path == "string") {
        var lookup = FS.lookupPath(path, { follow: true });
        node = lookup.node;
      } else {
        node = path;
      }
      if (!node.node_ops.setattr) {
        throw new FS.ErrnoError(63);
      }
      if (FS.isDir(node.mode)) {
        throw new FS.ErrnoError(31);
      }
      if (!FS.isFile(node.mode)) {
        throw new FS.ErrnoError(28);
      }
      var errCode = FS.nodePermissions(node, "w");
      if (errCode) {
        throw new FS.ErrnoError(errCode);
      }
      node.node_ops.setattr(node, { size: len, timestamp: Date.now() });
    }, ftruncate: (fd, len) => {
      var stream = FS.getStream(fd);
      if (!stream) {
        throw new FS.ErrnoError(8);
      }
      if ((stream.flags & 2097155) === 0) {
        throw new FS.ErrnoError(28);
      }
      FS.truncate(stream.node, len);
    }, utime: (path, atime, mtime) => {
      var lookup = FS.lookupPath(path, { follow: true });
      var node = lookup.node;
      node.node_ops.setattr(node, { timestamp: Math.max(atime, mtime) });
    }, open: (path, flags, mode) => {
      if (path === "") {
        throw new FS.ErrnoError(44);
      }
      flags = typeof flags == "string" ? FS.modeStringToFlags(flags) : flags;
      mode = typeof mode == "undefined" ? 438 : mode;
      if (flags & 64) {
        mode = mode & 4095 | 32768;
      } else {
        mode = 0;
      }
      var node;
      if (typeof path == "object") {
        node = path;
      } else {
        path = PATH.normalize(path);
        try {
          var lookup = FS.lookupPath(path, { follow: !(flags & 131072) });
          node = lookup.node;
        } catch (e) {}
      }
      var created = false;
      if (flags & 64) {
        if (node) {
          if (flags & 128) {
            throw new FS.ErrnoError(20);
          }
        } else {
          node = FS.mknod(path, mode, 0);
          created = true;
        }
      }
      if (!node) {
        throw new FS.ErrnoError(44);
      }
      if (FS.isChrdev(node.mode)) {
        flags &= ~512;
      }
      if (flags & 65536 && !FS.isDir(node.mode)) {
        throw new FS.ErrnoError(54);
      }
      if (!created) {
        var errCode = FS.mayOpen(node, flags);
        if (errCode) {
          throw new FS.ErrnoError(errCode);
        }
      }
      if (flags & 512 && !created) {
        FS.truncate(node, 0);
      }
      flags &= ~(128 | 512 | 131072);
      var stream = FS.createStream({ node, path: FS.getPath(node), flags, seekable: true, position: 0, stream_ops: node.stream_ops, ungotten: [], error: false });
      if (stream.stream_ops.open) {
        stream.stream_ops.open(stream);
      }
      if (Module2["logReadFiles"] && !(flags & 1)) {
        if (!FS.readFiles)
          FS.readFiles = {};
        if (!(path in FS.readFiles)) {
          FS.readFiles[path] = 1;
        }
      }
      return stream;
    }, close: (stream) => {
      if (FS.isClosed(stream)) {
        throw new FS.ErrnoError(8);
      }
      if (stream.getdents)
        stream.getdents = null;
      try {
        if (stream.stream_ops.close) {
          stream.stream_ops.close(stream);
        }
      } catch (e) {
        throw e;
      } finally {
        FS.closeStream(stream.fd);
      }
      stream.fd = null;
    }, isClosed: (stream) => {
      return stream.fd === null;
    }, llseek: (stream, offset, whence) => {
      if (FS.isClosed(stream)) {
        throw new FS.ErrnoError(8);
      }
      if (!stream.seekable || !stream.stream_ops.llseek) {
        throw new FS.ErrnoError(70);
      }
      if (whence != 0 && whence != 1 && whence != 2) {
        throw new FS.ErrnoError(28);
      }
      stream.position = stream.stream_ops.llseek(stream, offset, whence);
      stream.ungotten = [];
      return stream.position;
    }, read: (stream, buffer2, offset, length, position) => {
      if (length < 0 || position < 0) {
        throw new FS.ErrnoError(28);
      }
      if (FS.isClosed(stream)) {
        throw new FS.ErrnoError(8);
      }
      if ((stream.flags & 2097155) === 1) {
        throw new FS.ErrnoError(8);
      }
      if (FS.isDir(stream.node.mode)) {
        throw new FS.ErrnoError(31);
      }
      if (!stream.stream_ops.read) {
        throw new FS.ErrnoError(28);
      }
      var seeking = typeof position != "undefined";
      if (!seeking) {
        position = stream.position;
      } else if (!stream.seekable) {
        throw new FS.ErrnoError(70);
      }
      var bytesRead = stream.stream_ops.read(stream, buffer2, offset, length, position);
      if (!seeking)
        stream.position += bytesRead;
      return bytesRead;
    }, write: (stream, buffer2, offset, length, position, canOwn) => {
      if (length < 0 || position < 0) {
        throw new FS.ErrnoError(28);
      }
      if (FS.isClosed(stream)) {
        throw new FS.ErrnoError(8);
      }
      if ((stream.flags & 2097155) === 0) {
        throw new FS.ErrnoError(8);
      }
      if (FS.isDir(stream.node.mode)) {
        throw new FS.ErrnoError(31);
      }
      if (!stream.stream_ops.write) {
        throw new FS.ErrnoError(28);
      }
      if (stream.seekable && stream.flags & 1024) {
        FS.llseek(stream, 0, 2);
      }
      var seeking = typeof position != "undefined";
      if (!seeking) {
        position = stream.position;
      } else if (!stream.seekable) {
        throw new FS.ErrnoError(70);
      }
      var bytesWritten = stream.stream_ops.write(stream, buffer2, offset, length, position, canOwn);
      if (!seeking)
        stream.position += bytesWritten;
      return bytesWritten;
    }, allocate: (stream, offset, length) => {
      if (FS.isClosed(stream)) {
        throw new FS.ErrnoError(8);
      }
      if (offset < 0 || length <= 0) {
        throw new FS.ErrnoError(28);
      }
      if ((stream.flags & 2097155) === 0) {
        throw new FS.ErrnoError(8);
      }
      if (!FS.isFile(stream.node.mode) && !FS.isDir(stream.node.mode)) {
        throw new FS.ErrnoError(43);
      }
      if (!stream.stream_ops.allocate) {
        throw new FS.ErrnoError(138);
      }
      stream.stream_ops.allocate(stream, offset, length);
    }, mmap: (stream, length, position, prot, flags) => {
      if ((prot & 2) !== 0 && (flags & 2) === 0 && (stream.flags & 2097155) !== 2) {
        throw new FS.ErrnoError(2);
      }
      if ((stream.flags & 2097155) === 1) {
        throw new FS.ErrnoError(2);
      }
      if (!stream.stream_ops.mmap) {
        throw new FS.ErrnoError(43);
      }
      return stream.stream_ops.mmap(stream, length, position, prot, flags);
    }, msync: (stream, buffer2, offset, length, mmapFlags) => {
      if (!stream || !stream.stream_ops.msync) {
        return 0;
      }
      return stream.stream_ops.msync(stream, buffer2, offset, length, mmapFlags);
    }, munmap: (stream) => 0, ioctl: (stream, cmd, arg) => {
      if (!stream.stream_ops.ioctl) {
        throw new FS.ErrnoError(59);
      }
      return stream.stream_ops.ioctl(stream, cmd, arg);
    }, readFile: (path, opts = {}) => {
      opts.flags = opts.flags || 0;
      opts.encoding = opts.encoding || "binary";
      if (opts.encoding !== "utf8" && opts.encoding !== "binary") {
        throw new Error('Invalid encoding type "' + opts.encoding + '"');
      }
      var ret;
      var stream = FS.open(path, opts.flags);
      var stat = FS.stat(path);
      var length = stat.size;
      var buf = new Uint8Array(length);
      FS.read(stream, buf, 0, length, 0);
      if (opts.encoding === "utf8") {
        ret = UTF8ArrayToString(buf, 0);
      } else if (opts.encoding === "binary") {
        ret = buf;
      }
      FS.close(stream);
      return ret;
    }, writeFile: (path, data, opts = {}) => {
      opts.flags = opts.flags || 577;
      var stream = FS.open(path, opts.flags, opts.mode);
      if (typeof data == "string") {
        var buf = new Uint8Array(lengthBytesUTF8(data) + 1);
        var actualNumBytes = stringToUTF8Array(data, buf, 0, buf.length);
        FS.write(stream, buf, 0, actualNumBytes, undefined, opts.canOwn);
      } else if (ArrayBuffer.isView(data)) {
        FS.write(stream, data, 0, data.byteLength, undefined, opts.canOwn);
      } else {
        throw new Error("Unsupported data type");
      }
      FS.close(stream);
    }, cwd: () => FS.currentPath, chdir: (path) => {
      var lookup = FS.lookupPath(path, { follow: true });
      if (lookup.node === null) {
        throw new FS.ErrnoError(44);
      }
      if (!FS.isDir(lookup.node.mode)) {
        throw new FS.ErrnoError(54);
      }
      var errCode = FS.nodePermissions(lookup.node, "x");
      if (errCode) {
        throw new FS.ErrnoError(errCode);
      }
      FS.currentPath = lookup.path;
    }, createDefaultDirectories: () => {
      FS.mkdir("/tmp");
      FS.mkdir("/home");
      FS.mkdir("/home/web_user");
    }, createDefaultDevices: () => {
      FS.mkdir("/dev");
      FS.registerDevice(FS.makedev(1, 3), { read: () => 0, write: (stream, buffer2, offset, length, pos) => length });
      FS.mkdev("/dev/null", FS.makedev(1, 3));
      TTY.register(FS.makedev(5, 0), TTY.default_tty_ops);
      TTY.register(FS.makedev(6, 0), TTY.default_tty1_ops);
      FS.mkdev("/dev/tty", FS.makedev(5, 0));
      FS.mkdev("/dev/tty1", FS.makedev(6, 0));
      var random_device = getRandomDevice();
      FS.createDevice("/dev", "random", random_device);
      FS.createDevice("/dev", "urandom", random_device);
      FS.mkdir("/dev/shm");
      FS.mkdir("/dev/shm/tmp");
    }, createSpecialDirectories: () => {
      FS.mkdir("/proc");
      var proc_self = FS.mkdir("/proc/self");
      FS.mkdir("/proc/self/fd");
      FS.mount({ mount: () => {
        var node = FS.createNode(proc_self, "fd", 16384 | 511, 73);
        node.node_ops = { lookup: (parent, name) => {
          var fd = +name;
          var stream = FS.getStream(fd);
          if (!stream)
            throw new FS.ErrnoError(8);
          var ret = { parent: null, mount: { mountpoint: "fake" }, node_ops: { readlink: () => stream.path } };
          ret.parent = ret;
          return ret;
        } };
        return node;
      } }, {}, "/proc/self/fd");
    }, createStandardStreams: () => {
      if (Module2["stdin"]) {
        FS.createDevice("/dev", "stdin", Module2["stdin"]);
      } else {
        FS.symlink("/dev/tty", "/dev/stdin");
      }
      if (Module2["stdout"]) {
        FS.createDevice("/dev", "stdout", null, Module2["stdout"]);
      } else {
        FS.symlink("/dev/tty", "/dev/stdout");
      }
      if (Module2["stderr"]) {
        FS.createDevice("/dev", "stderr", null, Module2["stderr"]);
      } else {
        FS.symlink("/dev/tty1", "/dev/stderr");
      }
      var stdin = FS.open("/dev/stdin", 0);
      var stdout = FS.open("/dev/stdout", 1);
      var stderr = FS.open("/dev/stderr", 1);
    }, ensureErrnoError: () => {
      if (FS.ErrnoError)
        return;
      FS.ErrnoError = function ErrnoError(errno, node) {
        this.node = node;
        this.setErrno = function(errno2) {
          this.errno = errno2;
        };
        this.setErrno(errno);
        this.message = "FS error";
      };
      FS.ErrnoError.prototype = new Error;
      FS.ErrnoError.prototype.constructor = FS.ErrnoError;
      [44].forEach((code) => {
        FS.genericErrors[code] = new FS.ErrnoError(code);
        FS.genericErrors[code].stack = "<generic error, no stack>";
      });
    }, staticInit: () => {
      FS.ensureErrnoError();
      FS.nameTable = new Array(4096);
      FS.mount(MEMFS, {}, "/");
      FS.createDefaultDirectories();
      FS.createDefaultDevices();
      FS.createSpecialDirectories();
      FS.filesystems = { MEMFS };
    }, init: (input, output, error) => {
      FS.init.initialized = true;
      FS.ensureErrnoError();
      Module2["stdin"] = input || Module2["stdin"];
      Module2["stdout"] = output || Module2["stdout"];
      Module2["stderr"] = error || Module2["stderr"];
      FS.createStandardStreams();
    }, quit: () => {
      FS.init.initialized = false;
      for (var i2 = 0;i2 < FS.streams.length; i2++) {
        var stream = FS.streams[i2];
        if (!stream) {
          continue;
        }
        FS.close(stream);
      }
    }, getMode: (canRead, canWrite) => {
      var mode = 0;
      if (canRead)
        mode |= 292 | 73;
      if (canWrite)
        mode |= 146;
      return mode;
    }, findObject: (path, dontResolveLastLink) => {
      var ret = FS.analyzePath(path, dontResolveLastLink);
      if (ret.exists) {
        return ret.object;
      } else {
        return null;
      }
    }, analyzePath: (path, dontResolveLastLink) => {
      try {
        var lookup = FS.lookupPath(path, { follow: !dontResolveLastLink });
        path = lookup.path;
      } catch (e) {}
      var ret = { isRoot: false, exists: false, error: 0, name: null, path: null, object: null, parentExists: false, parentPath: null, parentObject: null };
      try {
        var lookup = FS.lookupPath(path, { parent: true });
        ret.parentExists = true;
        ret.parentPath = lookup.path;
        ret.parentObject = lookup.node;
        ret.name = PATH.basename(path);
        lookup = FS.lookupPath(path, { follow: !dontResolveLastLink });
        ret.exists = true;
        ret.path = lookup.path;
        ret.object = lookup.node;
        ret.name = lookup.node.name;
        ret.isRoot = lookup.path === "/";
      } catch (e) {
        ret.error = e.errno;
      }
      return ret;
    }, createPath: (parent, path, canRead, canWrite) => {
      parent = typeof parent == "string" ? parent : FS.getPath(parent);
      var parts = path.split("/").reverse();
      while (parts.length) {
        var part = parts.pop();
        if (!part)
          continue;
        var current = PATH.join2(parent, part);
        try {
          FS.mkdir(current);
        } catch (e) {}
        parent = current;
      }
      return current;
    }, createFile: (parent, name, properties, canRead, canWrite) => {
      var path = PATH.join2(typeof parent == "string" ? parent : FS.getPath(parent), name);
      var mode = FS.getMode(canRead, canWrite);
      return FS.create(path, mode);
    }, createDataFile: (parent, name, data, canRead, canWrite, canOwn) => {
      var path = name;
      if (parent) {
        parent = typeof parent == "string" ? parent : FS.getPath(parent);
        path = name ? PATH.join2(parent, name) : parent;
      }
      var mode = FS.getMode(canRead, canWrite);
      var node = FS.create(path, mode);
      if (data) {
        if (typeof data == "string") {
          var arr = new Array(data.length);
          for (var i2 = 0, len = data.length;i2 < len; ++i2)
            arr[i2] = data.charCodeAt(i2);
          data = arr;
        }
        FS.chmod(node, mode | 146);
        var stream = FS.open(node, 577);
        FS.write(stream, data, 0, data.length, 0, canOwn);
        FS.close(stream);
        FS.chmod(node, mode);
      }
      return node;
    }, createDevice: (parent, name, input, output) => {
      var path = PATH.join2(typeof parent == "string" ? parent : FS.getPath(parent), name);
      var mode = FS.getMode(!!input, !!output);
      if (!FS.createDevice.major)
        FS.createDevice.major = 64;
      var dev = FS.makedev(FS.createDevice.major++, 0);
      FS.registerDevice(dev, { open: (stream) => {
        stream.seekable = false;
      }, close: (stream) => {
        if (output && output.buffer && output.buffer.length) {
          output(10);
        }
      }, read: (stream, buffer2, offset, length, pos) => {
        var bytesRead = 0;
        for (var i2 = 0;i2 < length; i2++) {
          var result;
          try {
            result = input();
          } catch (e) {
            throw new FS.ErrnoError(29);
          }
          if (result === undefined && bytesRead === 0) {
            throw new FS.ErrnoError(6);
          }
          if (result === null || result === undefined)
            break;
          bytesRead++;
          buffer2[offset + i2] = result;
        }
        if (bytesRead) {
          stream.node.timestamp = Date.now();
        }
        return bytesRead;
      }, write: (stream, buffer2, offset, length, pos) => {
        for (var i2 = 0;i2 < length; i2++) {
          try {
            output(buffer2[offset + i2]);
          } catch (e) {
            throw new FS.ErrnoError(29);
          }
        }
        if (length) {
          stream.node.timestamp = Date.now();
        }
        return i2;
      } });
      return FS.mkdev(path, mode, dev);
    }, forceLoadFile: (obj) => {
      if (obj.isDevice || obj.isFolder || obj.link || obj.contents)
        return true;
      if (typeof XMLHttpRequest != "undefined") {
        throw new Error("Lazy loading should have been performed (contents set) in createLazyFile, but it was not. Lazy loading only works in web workers. Use --embed-file or --preload-file in emcc on the main thread.");
      } else if (read_) {
        try {
          obj.contents = intArrayFromString(read_(obj.url), true);
          obj.usedBytes = obj.contents.length;
        } catch (e) {
          throw new FS.ErrnoError(29);
        }
      } else {
        throw new Error("Cannot load without read() or XMLHttpRequest.");
      }
    }, createLazyFile: (parent, name, url, canRead, canWrite) => {
      function LazyUint8Array() {
        this.lengthKnown = false;
        this.chunks = [];
      }
      LazyUint8Array.prototype.get = function LazyUint8Array_get(idx) {
        if (idx > this.length - 1 || idx < 0) {
          return;
        }
        var chunkOffset = idx % this.chunkSize;
        var chunkNum = idx / this.chunkSize | 0;
        return this.getter(chunkNum)[chunkOffset];
      };
      LazyUint8Array.prototype.setDataGetter = function LazyUint8Array_setDataGetter(getter) {
        this.getter = getter;
      };
      LazyUint8Array.prototype.cacheLength = function LazyUint8Array_cacheLength() {
        var xhr = new XMLHttpRequest;
        xhr.open("HEAD", url, false);
        xhr.send(null);
        if (!(xhr.status >= 200 && xhr.status < 300 || xhr.status === 304))
          throw new Error("Couldn't load " + url + ". Status: " + xhr.status);
        var datalength = Number(xhr.getResponseHeader("Content-length"));
        var header;
        var hasByteServing = (header = xhr.getResponseHeader("Accept-Ranges")) && header === "bytes";
        var usesGzip = (header = xhr.getResponseHeader("Content-Encoding")) && header === "gzip";
        var chunkSize = 1024 * 1024;
        if (!hasByteServing)
          chunkSize = datalength;
        var doXHR = (from, to) => {
          if (from > to)
            throw new Error("invalid range (" + from + ", " + to + ") or no bytes requested!");
          if (to > datalength - 1)
            throw new Error("only " + datalength + " bytes available! programmer error!");
          var xhr2 = new XMLHttpRequest;
          xhr2.open("GET", url, false);
          if (datalength !== chunkSize)
            xhr2.setRequestHeader("Range", "bytes=" + from + "-" + to);
          xhr2.responseType = "arraybuffer";
          if (xhr2.overrideMimeType) {
            xhr2.overrideMimeType("text/plain; charset=x-user-defined");
          }
          xhr2.send(null);
          if (!(xhr2.status >= 200 && xhr2.status < 300 || xhr2.status === 304))
            throw new Error("Couldn't load " + url + ". Status: " + xhr2.status);
          if (xhr2.response !== undefined) {
            return new Uint8Array(xhr2.response || []);
          } else {
            return intArrayFromString(xhr2.responseText || "", true);
          }
        };
        var lazyArray2 = this;
        lazyArray2.setDataGetter((chunkNum) => {
          var start = chunkNum * chunkSize;
          var end = (chunkNum + 1) * chunkSize - 1;
          end = Math.min(end, datalength - 1);
          if (typeof lazyArray2.chunks[chunkNum] == "undefined") {
            lazyArray2.chunks[chunkNum] = doXHR(start, end);
          }
          if (typeof lazyArray2.chunks[chunkNum] == "undefined")
            throw new Error("doXHR failed!");
          return lazyArray2.chunks[chunkNum];
        });
        if (usesGzip || !datalength) {
          chunkSize = datalength = 1;
          datalength = this.getter(0).length;
          chunkSize = datalength;
          out("LazyFiles on gzip forces download of the whole file when length is accessed");
        }
        this._length = datalength;
        this._chunkSize = chunkSize;
        this.lengthKnown = true;
      };
      if (typeof XMLHttpRequest != "undefined") {
        if (!ENVIRONMENT_IS_WORKER)
          throw "Cannot do synchronous binary XHRs outside webworkers in modern browsers. Use --embed-file or --preload-file in emcc";
        var lazyArray = new LazyUint8Array;
        Object.defineProperties(lazyArray, { length: { get: function() {
          if (!this.lengthKnown) {
            this.cacheLength();
          }
          return this._length;
        } }, chunkSize: { get: function() {
          if (!this.lengthKnown) {
            this.cacheLength();
          }
          return this._chunkSize;
        } } });
        var properties = { isDevice: false, contents: lazyArray };
      } else {
        var properties = { isDevice: false, url };
      }
      var node = FS.createFile(parent, name, properties, canRead, canWrite);
      if (properties.contents) {
        node.contents = properties.contents;
      } else if (properties.url) {
        node.contents = null;
        node.url = properties.url;
      }
      Object.defineProperties(node, { usedBytes: { get: function() {
        return this.contents.length;
      } } });
      var stream_ops = {};
      var keys = Object.keys(node.stream_ops);
      keys.forEach((key) => {
        var fn = node.stream_ops[key];
        stream_ops[key] = function forceLoadLazyFile() {
          FS.forceLoadFile(node);
          return fn.apply(null, arguments);
        };
      });
      function writeChunks(stream, buffer2, offset, length, position) {
        var contents = stream.node.contents;
        if (position >= contents.length)
          return 0;
        var size = Math.min(contents.length - position, length);
        if (contents.slice) {
          for (var i2 = 0;i2 < size; i2++) {
            buffer2[offset + i2] = contents[position + i2];
          }
        } else {
          for (var i2 = 0;i2 < size; i2++) {
            buffer2[offset + i2] = contents.get(position + i2);
          }
        }
        return size;
      }
      stream_ops.read = (stream, buffer2, offset, length, position) => {
        FS.forceLoadFile(node);
        return writeChunks(stream, buffer2, offset, length, position);
      };
      stream_ops.mmap = (stream, length, position, prot, flags) => {
        FS.forceLoadFile(node);
        var ptr = mmapAlloc(length);
        if (!ptr) {
          throw new FS.ErrnoError(48);
        }
        writeChunks(stream, HEAP8, ptr, length, position);
        return { ptr, allocated: true };
      };
      node.stream_ops = stream_ops;
      return node;
    }, createPreloadedFile: (parent, name, url, canRead, canWrite, onload, onerror, dontCreateFile, canOwn, preFinish) => {
      var fullname = name ? PATH_FS.resolve(PATH.join2(parent, name)) : parent;
      var dep = getUniqueRunDependency("cp " + fullname);
      function processData(byteArray) {
        function finish(byteArray2) {
          if (preFinish)
            preFinish();
          if (!dontCreateFile) {
            FS.createDataFile(parent, name, byteArray2, canRead, canWrite, canOwn);
          }
          if (onload)
            onload();
          removeRunDependency(dep);
        }
        if (Browser.handledByPreloadPlugin(byteArray, fullname, finish, () => {
          if (onerror)
            onerror();
          removeRunDependency(dep);
        })) {
          return;
        }
        finish(byteArray);
      }
      addRunDependency(dep);
      if (typeof url == "string") {
        asyncLoad(url, (byteArray) => processData(byteArray), onerror);
      } else {
        processData(url);
      }
    }, indexedDB: () => {
      return window.indexedDB || window.mozIndexedDB || window.webkitIndexedDB || window.msIndexedDB;
    }, DB_NAME: () => {
      return "EM_FS_" + window.location.pathname;
    }, DB_VERSION: 20, DB_STORE_NAME: "FILE_DATA", saveFilesToDB: (paths, onload, onerror) => {
      onload = onload || (() => {});
      onerror = onerror || (() => {});
      var indexedDB = FS.indexedDB();
      try {
        var openRequest = indexedDB.open(FS.DB_NAME(), FS.DB_VERSION);
      } catch (e) {
        return onerror(e);
      }
      openRequest.onupgradeneeded = () => {
        out("creating db");
        var db = openRequest.result;
        db.createObjectStore(FS.DB_STORE_NAME);
      };
      openRequest.onsuccess = () => {
        var db = openRequest.result;
        var transaction = db.transaction([FS.DB_STORE_NAME], "readwrite");
        var files = transaction.objectStore(FS.DB_STORE_NAME);
        var ok = 0, fail = 0, total = paths.length;
        function finish() {
          if (fail == 0)
            onload();
          else
            onerror();
        }
        paths.forEach((path) => {
          var putRequest = files.put(FS.analyzePath(path).object.contents, path);
          putRequest.onsuccess = () => {
            ok++;
            if (ok + fail == total)
              finish();
          };
          putRequest.onerror = () => {
            fail++;
            if (ok + fail == total)
              finish();
          };
        });
        transaction.onerror = onerror;
      };
      openRequest.onerror = onerror;
    }, loadFilesFromDB: (paths, onload, onerror) => {
      onload = onload || (() => {});
      onerror = onerror || (() => {});
      var indexedDB = FS.indexedDB();
      try {
        var openRequest = indexedDB.open(FS.DB_NAME(), FS.DB_VERSION);
      } catch (e) {
        return onerror(e);
      }
      openRequest.onupgradeneeded = onerror;
      openRequest.onsuccess = () => {
        var db = openRequest.result;
        try {
          var transaction = db.transaction([FS.DB_STORE_NAME], "readonly");
        } catch (e) {
          onerror(e);
          return;
        }
        var files = transaction.objectStore(FS.DB_STORE_NAME);
        var ok = 0, fail = 0, total = paths.length;
        function finish() {
          if (fail == 0)
            onload();
          else
            onerror();
        }
        paths.forEach((path) => {
          var getRequest = files.get(path);
          getRequest.onsuccess = () => {
            if (FS.analyzePath(path).exists) {
              FS.unlink(path);
            }
            FS.createDataFile(PATH.dirname(path), PATH.basename(path), getRequest.result, true, true, true);
            ok++;
            if (ok + fail == total)
              finish();
          };
          getRequest.onerror = () => {
            fail++;
            if (ok + fail == total)
              finish();
          };
        });
        transaction.onerror = onerror;
      };
      openRequest.onerror = onerror;
    } };
    var SYSCALLS = { DEFAULT_POLLMASK: 5, calculateAt: function(dirfd, path, allowEmpty) {
      if (PATH.isAbs(path)) {
        return path;
      }
      var dir;
      if (dirfd === -100) {
        dir = FS.cwd();
      } else {
        var dirstream = FS.getStream(dirfd);
        if (!dirstream)
          throw new FS.ErrnoError(8);
        dir = dirstream.path;
      }
      if (path.length == 0) {
        if (!allowEmpty) {
          throw new FS.ErrnoError(44);
        }
        return dir;
      }
      return PATH.join2(dir, path);
    }, doStat: function(func, path, buf) {
      try {
        var stat = func(path);
      } catch (e) {
        if (e && e.node && PATH.normalize(path) !== PATH.normalize(FS.getPath(e.node))) {
          return -54;
        }
        throw e;
      }
      HEAP32[buf >> 2] = stat.dev;
      HEAP32[buf + 4 >> 2] = 0;
      HEAP32[buf + 8 >> 2] = stat.ino;
      HEAP32[buf + 12 >> 2] = stat.mode;
      HEAP32[buf + 16 >> 2] = stat.nlink;
      HEAP32[buf + 20 >> 2] = stat.uid;
      HEAP32[buf + 24 >> 2] = stat.gid;
      HEAP32[buf + 28 >> 2] = stat.rdev;
      HEAP32[buf + 32 >> 2] = 0;
      tempI64 = [stat.size >>> 0, (tempDouble = stat.size, +Math.abs(tempDouble) >= 1 ? tempDouble > 0 ? (Math.min(+Math.floor(tempDouble / 4294967296), 4294967295) | 0) >>> 0 : ~~+Math.ceil((tempDouble - +(~~tempDouble >>> 0)) / 4294967296) >>> 0 : 0)], HEAP32[buf + 40 >> 2] = tempI64[0], HEAP32[buf + 44 >> 2] = tempI64[1];
      HEAP32[buf + 48 >> 2] = 4096;
      HEAP32[buf + 52 >> 2] = stat.blocks;
      HEAP32[buf + 56 >> 2] = stat.atime.getTime() / 1000 | 0;
      HEAP32[buf + 60 >> 2] = 0;
      HEAP32[buf + 64 >> 2] = stat.mtime.getTime() / 1000 | 0;
      HEAP32[buf + 68 >> 2] = 0;
      HEAP32[buf + 72 >> 2] = stat.ctime.getTime() / 1000 | 0;
      HEAP32[buf + 76 >> 2] = 0;
      tempI64 = [stat.ino >>> 0, (tempDouble = stat.ino, +Math.abs(tempDouble) >= 1 ? tempDouble > 0 ? (Math.min(+Math.floor(tempDouble / 4294967296), 4294967295) | 0) >>> 0 : ~~+Math.ceil((tempDouble - +(~~tempDouble >>> 0)) / 4294967296) >>> 0 : 0)], HEAP32[buf + 80 >> 2] = tempI64[0], HEAP32[buf + 84 >> 2] = tempI64[1];
      return 0;
    }, doMsync: function(addr, stream, len, flags, offset) {
      var buffer2 = HEAPU8.slice(addr, addr + len);
      FS.msync(stream, buffer2, offset, len, flags);
    }, varargs: undefined, get: function() {
      SYSCALLS.varargs += 4;
      var ret = HEAP32[SYSCALLS.varargs - 4 >> 2];
      return ret;
    }, getStr: function(ptr) {
      var ret = UTF8ToString(ptr);
      return ret;
    }, getStreamFromFD: function(fd) {
      var stream = FS.getStream(fd);
      if (!stream)
        throw new FS.ErrnoError(8);
      return stream;
    } };
    function ___syscall_chmod(path, mode) {
      try {
        path = SYSCALLS.getStr(path);
        FS.chmod(path, mode);
        return 0;
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function ___syscall_faccessat(dirfd, path, amode, flags) {
      try {
        path = SYSCALLS.getStr(path);
        path = SYSCALLS.calculateAt(dirfd, path);
        if (amode & ~7) {
          return -28;
        }
        var lookup = FS.lookupPath(path, { follow: true });
        var node = lookup.node;
        if (!node) {
          return -44;
        }
        var perms = "";
        if (amode & 4)
          perms += "r";
        if (amode & 2)
          perms += "w";
        if (amode & 1)
          perms += "x";
        if (perms && FS.nodePermissions(node, perms)) {
          return -2;
        }
        return 0;
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function setErrNo(value) {
      HEAP32[___errno_location() >> 2] = value;
      return value;
    }
    function ___syscall_fcntl64(fd, cmd, varargs) {
      SYSCALLS.varargs = varargs;
      try {
        var stream = SYSCALLS.getStreamFromFD(fd);
        switch (cmd) {
          case 0: {
            var arg = SYSCALLS.get();
            if (arg < 0) {
              return -28;
            }
            var newStream;
            newStream = FS.createStream(stream, arg);
            return newStream.fd;
          }
          case 1:
          case 2:
            return 0;
          case 3:
            return stream.flags;
          case 4: {
            var arg = SYSCALLS.get();
            stream.flags |= arg;
            return 0;
          }
          case 5: {
            var arg = SYSCALLS.get();
            var offset = 0;
            HEAP16[arg + offset >> 1] = 2;
            return 0;
          }
          case 6:
          case 7:
            return 0;
          case 16:
          case 8:
            return -28;
          case 9:
            setErrNo(28);
            return -1;
          default: {
            return -28;
          }
        }
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function ___syscall_fstat64(fd, buf) {
      try {
        var stream = SYSCALLS.getStreamFromFD(fd);
        return SYSCALLS.doStat(FS.stat, stream.path, buf);
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function ___syscall_ioctl(fd, op, varargs) {
      SYSCALLS.varargs = varargs;
      try {
        var stream = SYSCALLS.getStreamFromFD(fd);
        switch (op) {
          case 21509:
          case 21505: {
            if (!stream.tty)
              return -59;
            return 0;
          }
          case 21510:
          case 21511:
          case 21512:
          case 21506:
          case 21507:
          case 21508: {
            if (!stream.tty)
              return -59;
            return 0;
          }
          case 21519: {
            if (!stream.tty)
              return -59;
            var argp = SYSCALLS.get();
            HEAP32[argp >> 2] = 0;
            return 0;
          }
          case 21520: {
            if (!stream.tty)
              return -59;
            return -28;
          }
          case 21531: {
            var argp = SYSCALLS.get();
            return FS.ioctl(stream, op, argp);
          }
          case 21523: {
            if (!stream.tty)
              return -59;
            return 0;
          }
          case 21524: {
            if (!stream.tty)
              return -59;
            return 0;
          }
          default:
            abort("bad ioctl syscall " + op);
        }
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function ___syscall_lstat64(path, buf) {
      try {
        path = SYSCALLS.getStr(path);
        return SYSCALLS.doStat(FS.lstat, path, buf);
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function ___syscall_newfstatat(dirfd, path, buf, flags) {
      try {
        path = SYSCALLS.getStr(path);
        var nofollow = flags & 256;
        var allowEmpty = flags & 4096;
        flags = flags & ~4352;
        path = SYSCALLS.calculateAt(dirfd, path, allowEmpty);
        return SYSCALLS.doStat(nofollow ? FS.lstat : FS.stat, path, buf);
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function ___syscall_openat(dirfd, path, flags, varargs) {
      SYSCALLS.varargs = varargs;
      try {
        path = SYSCALLS.getStr(path);
        path = SYSCALLS.calculateAt(dirfd, path);
        var mode = varargs ? SYSCALLS.get() : 0;
        return FS.open(path, flags, mode).fd;
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function ___syscall_stat64(path, buf) {
      try {
        path = SYSCALLS.getStr(path);
        return SYSCALLS.doStat(FS.stat, path, buf);
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function __embind_register_bigint(primitiveType, name, size, minRange, maxRange) {}
    function getShiftFromSize(size) {
      switch (size) {
        case 1:
          return 0;
        case 2:
          return 1;
        case 4:
          return 2;
        case 8:
          return 3;
        default:
          throw new TypeError("Unknown type size: " + size);
      }
    }
    function embind_init_charCodes() {
      var codes = new Array(256);
      for (var i2 = 0;i2 < 256; ++i2) {
        codes[i2] = String.fromCharCode(i2);
      }
      embind_charCodes = codes;
    }
    var embind_charCodes = undefined;
    function readLatin1String(ptr) {
      var ret = "";
      var c = ptr;
      while (HEAPU8[c]) {
        ret += embind_charCodes[HEAPU8[c++]];
      }
      return ret;
    }
    var awaitingDependencies = {};
    var registeredTypes = {};
    var typeDependencies = {};
    var char_0 = 48;
    var char_9 = 57;
    function makeLegalFunctionName(name) {
      if (name === undefined) {
        return "_unknown";
      }
      name = name.replace(/[^a-zA-Z0-9_]/g, "$");
      var f = name.charCodeAt(0);
      if (f >= char_0 && f <= char_9) {
        return "_" + name;
      }
      return name;
    }
    function createNamedFunction(name, body) {
      name = makeLegalFunctionName(name);
      return new Function("body", "return function " + name + `() {
` + '    "use strict";' + `    return body.apply(this, arguments);
` + `};
`)(body);
    }
    function extendError(baseErrorType, errorName) {
      var errorClass = createNamedFunction(errorName, function(message) {
        this.name = errorName;
        this.message = message;
        var stack = new Error(message).stack;
        if (stack !== undefined) {
          this.stack = this.toString() + `
` + stack.replace(/^Error(:[^\n]*)?\n/, "");
        }
      });
      errorClass.prototype = Object.create(baseErrorType.prototype);
      errorClass.prototype.constructor = errorClass;
      errorClass.prototype.toString = function() {
        if (this.message === undefined) {
          return this.name;
        } else {
          return this.name + ": " + this.message;
        }
      };
      return errorClass;
    }
    var BindingError = undefined;
    function throwBindingError(message) {
      throw new BindingError(message);
    }
    var InternalError = undefined;
    function throwInternalError(message) {
      throw new InternalError(message);
    }
    function whenDependentTypesAreResolved(myTypes, dependentTypes, getTypeConverters) {
      myTypes.forEach(function(type) {
        typeDependencies[type] = dependentTypes;
      });
      function onComplete(typeConverters2) {
        var myTypeConverters = getTypeConverters(typeConverters2);
        if (myTypeConverters.length !== myTypes.length) {
          throwInternalError("Mismatched type converter count");
        }
        for (var i2 = 0;i2 < myTypes.length; ++i2) {
          registerType(myTypes[i2], myTypeConverters[i2]);
        }
      }
      var typeConverters = new Array(dependentTypes.length);
      var unregisteredTypes = [];
      var registered = 0;
      dependentTypes.forEach((dt, i2) => {
        if (registeredTypes.hasOwnProperty(dt)) {
          typeConverters[i2] = registeredTypes[dt];
        } else {
          unregisteredTypes.push(dt);
          if (!awaitingDependencies.hasOwnProperty(dt)) {
            awaitingDependencies[dt] = [];
          }
          awaitingDependencies[dt].push(() => {
            typeConverters[i2] = registeredTypes[dt];
            ++registered;
            if (registered === unregisteredTypes.length) {
              onComplete(typeConverters);
            }
          });
        }
      });
      if (unregisteredTypes.length === 0) {
        onComplete(typeConverters);
      }
    }
    function registerType(rawType, registeredInstance, options = {}) {
      if (!("argPackAdvance" in registeredInstance)) {
        throw new TypeError("registerType registeredInstance requires argPackAdvance");
      }
      var name = registeredInstance.name;
      if (!rawType) {
        throwBindingError('type "' + name + '" must have a positive integer typeid pointer');
      }
      if (registeredTypes.hasOwnProperty(rawType)) {
        if (options.ignoreDuplicateRegistrations) {
          return;
        } else {
          throwBindingError("Cannot register type '" + name + "' twice");
        }
      }
      registeredTypes[rawType] = registeredInstance;
      delete typeDependencies[rawType];
      if (awaitingDependencies.hasOwnProperty(rawType)) {
        var callbacks = awaitingDependencies[rawType];
        delete awaitingDependencies[rawType];
        callbacks.forEach((cb) => cb());
      }
    }
    function __embind_register_bool(rawType, name, size, trueValue, falseValue) {
      var shift = getShiftFromSize(size);
      name = readLatin1String(name);
      registerType(rawType, { name, fromWireType: function(wt) {
        return !!wt;
      }, toWireType: function(destructors, o) {
        return o ? trueValue : falseValue;
      }, argPackAdvance: 8, readValueFromPointer: function(pointer) {
        var heap;
        if (size === 1) {
          heap = HEAP8;
        } else if (size === 2) {
          heap = HEAP16;
        } else if (size === 4) {
          heap = HEAP32;
        } else {
          throw new TypeError("Unknown boolean type size: " + name);
        }
        return this["fromWireType"](heap[pointer >> shift]);
      }, destructorFunction: null });
    }
    function ClassHandle_isAliasOf(other) {
      if (!(this instanceof ClassHandle)) {
        return false;
      }
      if (!(other instanceof ClassHandle)) {
        return false;
      }
      var leftClass = this.$$.ptrType.registeredClass;
      var left = this.$$.ptr;
      var rightClass = other.$$.ptrType.registeredClass;
      var right = other.$$.ptr;
      while (leftClass.baseClass) {
        left = leftClass.upcast(left);
        leftClass = leftClass.baseClass;
      }
      while (rightClass.baseClass) {
        right = rightClass.upcast(right);
        rightClass = rightClass.baseClass;
      }
      return leftClass === rightClass && left === right;
    }
    function shallowCopyInternalPointer(o) {
      return { count: o.count, deleteScheduled: o.deleteScheduled, preservePointerOnDelete: o.preservePointerOnDelete, ptr: o.ptr, ptrType: o.ptrType, smartPtr: o.smartPtr, smartPtrType: o.smartPtrType };
    }
    function throwInstanceAlreadyDeleted(obj) {
      function getInstanceTypeName(handle) {
        return handle.$$.ptrType.registeredClass.name;
      }
      throwBindingError(getInstanceTypeName(obj) + " instance already deleted");
    }
    var finalizationRegistry = false;
    function detachFinalizer(handle) {}
    function runDestructor($$) {
      if ($$.smartPtr) {
        $$.smartPtrType.rawDestructor($$.smartPtr);
      } else {
        $$.ptrType.registeredClass.rawDestructor($$.ptr);
      }
    }
    function releaseClassHandle($$) {
      $$.count.value -= 1;
      var toDelete = $$.count.value === 0;
      if (toDelete) {
        runDestructor($$);
      }
    }
    function downcastPointer(ptr, ptrClass, desiredClass) {
      if (ptrClass === desiredClass) {
        return ptr;
      }
      if (desiredClass.baseClass === undefined) {
        return null;
      }
      var rv = downcastPointer(ptr, ptrClass, desiredClass.baseClass);
      if (rv === null) {
        return null;
      }
      return desiredClass.downcast(rv);
    }
    var registeredPointers = {};
    function getInheritedInstanceCount() {
      return Object.keys(registeredInstances).length;
    }
    function getLiveInheritedInstances() {
      var rv = [];
      for (var k in registeredInstances) {
        if (registeredInstances.hasOwnProperty(k)) {
          rv.push(registeredInstances[k]);
        }
      }
      return rv;
    }
    var deletionQueue = [];
    function flushPendingDeletes() {
      while (deletionQueue.length) {
        var obj = deletionQueue.pop();
        obj.$$.deleteScheduled = false;
        obj["delete"]();
      }
    }
    var delayFunction = undefined;
    function setDelayFunction(fn) {
      delayFunction = fn;
      if (deletionQueue.length && delayFunction) {
        delayFunction(flushPendingDeletes);
      }
    }
    function init_embind() {
      Module2["getInheritedInstanceCount"] = getInheritedInstanceCount;
      Module2["getLiveInheritedInstances"] = getLiveInheritedInstances;
      Module2["flushPendingDeletes"] = flushPendingDeletes;
      Module2["setDelayFunction"] = setDelayFunction;
    }
    var registeredInstances = {};
    function getBasestPointer(class_, ptr) {
      if (ptr === undefined) {
        throwBindingError("ptr should not be undefined");
      }
      while (class_.baseClass) {
        ptr = class_.upcast(ptr);
        class_ = class_.baseClass;
      }
      return ptr;
    }
    function getInheritedInstance(class_, ptr) {
      ptr = getBasestPointer(class_, ptr);
      return registeredInstances[ptr];
    }
    function makeClassHandle(prototype, record) {
      if (!record.ptrType || !record.ptr) {
        throwInternalError("makeClassHandle requires ptr and ptrType");
      }
      var hasSmartPtrType = !!record.smartPtrType;
      var hasSmartPtr = !!record.smartPtr;
      if (hasSmartPtrType !== hasSmartPtr) {
        throwInternalError("Both smartPtrType and smartPtr must be specified");
      }
      record.count = { value: 1 };
      return attachFinalizer(Object.create(prototype, { $$: { value: record } }));
    }
    function RegisteredPointer_fromWireType(ptr) {
      var rawPointer = this.getPointee(ptr);
      if (!rawPointer) {
        this.destructor(ptr);
        return null;
      }
      var registeredInstance = getInheritedInstance(this.registeredClass, rawPointer);
      if (registeredInstance !== undefined) {
        if (registeredInstance.$$.count.value === 0) {
          registeredInstance.$$.ptr = rawPointer;
          registeredInstance.$$.smartPtr = ptr;
          return registeredInstance["clone"]();
        } else {
          var rv = registeredInstance["clone"]();
          this.destructor(ptr);
          return rv;
        }
      }
      function makeDefaultHandle() {
        if (this.isSmartPointer) {
          return makeClassHandle(this.registeredClass.instancePrototype, { ptrType: this.pointeeType, ptr: rawPointer, smartPtrType: this, smartPtr: ptr });
        } else {
          return makeClassHandle(this.registeredClass.instancePrototype, { ptrType: this, ptr });
        }
      }
      var actualType = this.registeredClass.getActualType(rawPointer);
      var registeredPointerRecord = registeredPointers[actualType];
      if (!registeredPointerRecord) {
        return makeDefaultHandle.call(this);
      }
      var toType;
      if (this.isConst) {
        toType = registeredPointerRecord.constPointerType;
      } else {
        toType = registeredPointerRecord.pointerType;
      }
      var dp = downcastPointer(rawPointer, this.registeredClass, toType.registeredClass);
      if (dp === null) {
        return makeDefaultHandle.call(this);
      }
      if (this.isSmartPointer) {
        return makeClassHandle(toType.registeredClass.instancePrototype, { ptrType: toType, ptr: dp, smartPtrType: this, smartPtr: ptr });
      } else {
        return makeClassHandle(toType.registeredClass.instancePrototype, { ptrType: toType, ptr: dp });
      }
    }
    function attachFinalizer(handle) {
      if (typeof FinalizationRegistry === "undefined") {
        attachFinalizer = (handle2) => handle2;
        return handle;
      }
      finalizationRegistry = new FinalizationRegistry((info) => {
        releaseClassHandle(info.$$);
      });
      attachFinalizer = (handle2) => {
        var $$ = handle2.$$;
        var hasSmartPtr = !!$$.smartPtr;
        if (hasSmartPtr) {
          var info = { $$ };
          finalizationRegistry.register(handle2, info, handle2);
        }
        return handle2;
      };
      detachFinalizer = (handle2) => finalizationRegistry.unregister(handle2);
      return attachFinalizer(handle);
    }
    function ClassHandle_clone() {
      if (!this.$$.ptr) {
        throwInstanceAlreadyDeleted(this);
      }
      if (this.$$.preservePointerOnDelete) {
        this.$$.count.value += 1;
        return this;
      } else {
        var clone = attachFinalizer(Object.create(Object.getPrototypeOf(this), { $$: { value: shallowCopyInternalPointer(this.$$) } }));
        clone.$$.count.value += 1;
        clone.$$.deleteScheduled = false;
        return clone;
      }
    }
    function ClassHandle_delete() {
      if (!this.$$.ptr) {
        throwInstanceAlreadyDeleted(this);
      }
      if (this.$$.deleteScheduled && !this.$$.preservePointerOnDelete) {
        throwBindingError("Object already scheduled for deletion");
      }
      detachFinalizer(this);
      releaseClassHandle(this.$$);
      if (!this.$$.preservePointerOnDelete) {
        this.$$.smartPtr = undefined;
        this.$$.ptr = undefined;
      }
    }
    function ClassHandle_isDeleted() {
      return !this.$$.ptr;
    }
    function ClassHandle_deleteLater() {
      if (!this.$$.ptr) {
        throwInstanceAlreadyDeleted(this);
      }
      if (this.$$.deleteScheduled && !this.$$.preservePointerOnDelete) {
        throwBindingError("Object already scheduled for deletion");
      }
      deletionQueue.push(this);
      if (deletionQueue.length === 1 && delayFunction) {
        delayFunction(flushPendingDeletes);
      }
      this.$$.deleteScheduled = true;
      return this;
    }
    function init_ClassHandle() {
      ClassHandle.prototype["isAliasOf"] = ClassHandle_isAliasOf;
      ClassHandle.prototype["clone"] = ClassHandle_clone;
      ClassHandle.prototype["delete"] = ClassHandle_delete;
      ClassHandle.prototype["isDeleted"] = ClassHandle_isDeleted;
      ClassHandle.prototype["deleteLater"] = ClassHandle_deleteLater;
    }
    function ClassHandle() {}
    function ensureOverloadTable(proto, methodName, humanName) {
      if (proto[methodName].overloadTable === undefined) {
        var prevFunc = proto[methodName];
        proto[methodName] = function() {
          if (!proto[methodName].overloadTable.hasOwnProperty(arguments.length)) {
            throwBindingError("Function '" + humanName + "' called with an invalid number of arguments (" + arguments.length + ") - expects one of (" + proto[methodName].overloadTable + ")!");
          }
          return proto[methodName].overloadTable[arguments.length].apply(this, arguments);
        };
        proto[methodName].overloadTable = [];
        proto[methodName].overloadTable[prevFunc.argCount] = prevFunc;
      }
    }
    function exposePublicSymbol(name, value, numArguments) {
      if (Module2.hasOwnProperty(name)) {
        if (numArguments === undefined || Module2[name].overloadTable !== undefined && Module2[name].overloadTable[numArguments] !== undefined) {
          throwBindingError("Cannot register public name '" + name + "' twice");
        }
        ensureOverloadTable(Module2, name, name);
        if (Module2.hasOwnProperty(numArguments)) {
          throwBindingError("Cannot register multiple overloads of a function with the same number of arguments (" + numArguments + ")!");
        }
        Module2[name].overloadTable[numArguments] = value;
      } else {
        Module2[name] = value;
        if (numArguments !== undefined) {
          Module2[name].numArguments = numArguments;
        }
      }
    }
    function RegisteredClass(name, constructor, instancePrototype, rawDestructor, baseClass, getActualType, upcast, downcast) {
      this.name = name;
      this.constructor = constructor;
      this.instancePrototype = instancePrototype;
      this.rawDestructor = rawDestructor;
      this.baseClass = baseClass;
      this.getActualType = getActualType;
      this.upcast = upcast;
      this.downcast = downcast;
      this.pureVirtualFunctions = [];
    }
    function upcastPointer(ptr, ptrClass, desiredClass) {
      while (ptrClass !== desiredClass) {
        if (!ptrClass.upcast) {
          throwBindingError("Expected null or instance of " + desiredClass.name + ", got an instance of " + ptrClass.name);
        }
        ptr = ptrClass.upcast(ptr);
        ptrClass = ptrClass.baseClass;
      }
      return ptr;
    }
    function constNoSmartPtrRawPointerToWireType(destructors, handle) {
      if (handle === null) {
        if (this.isReference) {
          throwBindingError("null is not a valid " + this.name);
        }
        return 0;
      }
      if (!handle.$$) {
        throwBindingError('Cannot pass "' + _embind_repr(handle) + '" as a ' + this.name);
      }
      if (!handle.$$.ptr) {
        throwBindingError("Cannot pass deleted object as a pointer of type " + this.name);
      }
      var handleClass = handle.$$.ptrType.registeredClass;
      var ptr = upcastPointer(handle.$$.ptr, handleClass, this.registeredClass);
      return ptr;
    }
    function genericPointerToWireType(destructors, handle) {
      var ptr;
      if (handle === null) {
        if (this.isReference) {
          throwBindingError("null is not a valid " + this.name);
        }
        if (this.isSmartPointer) {
          ptr = this.rawConstructor();
          if (destructors !== null) {
            destructors.push(this.rawDestructor, ptr);
          }
          return ptr;
        } else {
          return 0;
        }
      }
      if (!handle.$$) {
        throwBindingError('Cannot pass "' + _embind_repr(handle) + '" as a ' + this.name);
      }
      if (!handle.$$.ptr) {
        throwBindingError("Cannot pass deleted object as a pointer of type " + this.name);
      }
      if (!this.isConst && handle.$$.ptrType.isConst) {
        throwBindingError("Cannot convert argument of type " + (handle.$$.smartPtrType ? handle.$$.smartPtrType.name : handle.$$.ptrType.name) + " to parameter type " + this.name);
      }
      var handleClass = handle.$$.ptrType.registeredClass;
      ptr = upcastPointer(handle.$$.ptr, handleClass, this.registeredClass);
      if (this.isSmartPointer) {
        if (handle.$$.smartPtr === undefined) {
          throwBindingError("Passing raw pointer to smart pointer is illegal");
        }
        switch (this.sharingPolicy) {
          case 0:
            if (handle.$$.smartPtrType === this) {
              ptr = handle.$$.smartPtr;
            } else {
              throwBindingError("Cannot convert argument of type " + (handle.$$.smartPtrType ? handle.$$.smartPtrType.name : handle.$$.ptrType.name) + " to parameter type " + this.name);
            }
            break;
          case 1:
            ptr = handle.$$.smartPtr;
            break;
          case 2:
            if (handle.$$.smartPtrType === this) {
              ptr = handle.$$.smartPtr;
            } else {
              var clonedHandle = handle["clone"]();
              ptr = this.rawShare(ptr, Emval.toHandle(function() {
                clonedHandle["delete"]();
              }));
              if (destructors !== null) {
                destructors.push(this.rawDestructor, ptr);
              }
            }
            break;
          default:
            throwBindingError("Unsupporting sharing policy");
        }
      }
      return ptr;
    }
    function nonConstNoSmartPtrRawPointerToWireType(destructors, handle) {
      if (handle === null) {
        if (this.isReference) {
          throwBindingError("null is not a valid " + this.name);
        }
        return 0;
      }
      if (!handle.$$) {
        throwBindingError('Cannot pass "' + _embind_repr(handle) + '" as a ' + this.name);
      }
      if (!handle.$$.ptr) {
        throwBindingError("Cannot pass deleted object as a pointer of type " + this.name);
      }
      if (handle.$$.ptrType.isConst) {
        throwBindingError("Cannot convert argument of type " + handle.$$.ptrType.name + " to parameter type " + this.name);
      }
      var handleClass = handle.$$.ptrType.registeredClass;
      var ptr = upcastPointer(handle.$$.ptr, handleClass, this.registeredClass);
      return ptr;
    }
    function simpleReadValueFromPointer(pointer) {
      return this["fromWireType"](HEAPU32[pointer >> 2]);
    }
    function RegisteredPointer_getPointee(ptr) {
      if (this.rawGetPointee) {
        ptr = this.rawGetPointee(ptr);
      }
      return ptr;
    }
    function RegisteredPointer_destructor(ptr) {
      if (this.rawDestructor) {
        this.rawDestructor(ptr);
      }
    }
    function RegisteredPointer_deleteObject(handle) {
      if (handle !== null) {
        handle["delete"]();
      }
    }
    function init_RegisteredPointer() {
      RegisteredPointer.prototype.getPointee = RegisteredPointer_getPointee;
      RegisteredPointer.prototype.destructor = RegisteredPointer_destructor;
      RegisteredPointer.prototype["argPackAdvance"] = 8;
      RegisteredPointer.prototype["readValueFromPointer"] = simpleReadValueFromPointer;
      RegisteredPointer.prototype["deleteObject"] = RegisteredPointer_deleteObject;
      RegisteredPointer.prototype["fromWireType"] = RegisteredPointer_fromWireType;
    }
    function RegisteredPointer(name, registeredClass, isReference, isConst, isSmartPointer, pointeeType, sharingPolicy, rawGetPointee, rawConstructor, rawShare, rawDestructor) {
      this.name = name;
      this.registeredClass = registeredClass;
      this.isReference = isReference;
      this.isConst = isConst;
      this.isSmartPointer = isSmartPointer;
      this.pointeeType = pointeeType;
      this.sharingPolicy = sharingPolicy;
      this.rawGetPointee = rawGetPointee;
      this.rawConstructor = rawConstructor;
      this.rawShare = rawShare;
      this.rawDestructor = rawDestructor;
      if (!isSmartPointer && registeredClass.baseClass === undefined) {
        if (isConst) {
          this["toWireType"] = constNoSmartPtrRawPointerToWireType;
          this.destructorFunction = null;
        } else {
          this["toWireType"] = nonConstNoSmartPtrRawPointerToWireType;
          this.destructorFunction = null;
        }
      } else {
        this["toWireType"] = genericPointerToWireType;
      }
    }
    function replacePublicSymbol(name, value, numArguments) {
      if (!Module2.hasOwnProperty(name)) {
        throwInternalError("Replacing nonexistant public symbol");
      }
      if (Module2[name].overloadTable !== undefined && numArguments !== undefined) {
        Module2[name].overloadTable[numArguments] = value;
      } else {
        Module2[name] = value;
        Module2[name].argCount = numArguments;
      }
    }
    function dynCallLegacy(sig, ptr, args) {
      var f = Module2["dynCall_" + sig];
      return args && args.length ? f.apply(null, [ptr].concat(args)) : f.call(null, ptr);
    }
    function dynCall(sig, ptr, args) {
      if (sig.includes("j")) {
        return dynCallLegacy(sig, ptr, args);
      }
      return getWasmTableEntry(ptr).apply(null, args);
    }
    function getDynCaller(sig, ptr) {
      var argCache = [];
      return function() {
        argCache.length = 0;
        Object.assign(argCache, arguments);
        return dynCall(sig, ptr, argCache);
      };
    }
    function embind__requireFunction(signature, rawFunction) {
      signature = readLatin1String(signature);
      function makeDynCaller() {
        if (signature.includes("j")) {
          return getDynCaller(signature, rawFunction);
        }
        return getWasmTableEntry(rawFunction);
      }
      var fp = makeDynCaller();
      if (typeof fp != "function") {
        throwBindingError("unknown function pointer with signature " + signature + ": " + rawFunction);
      }
      return fp;
    }
    var UnboundTypeError = undefined;
    function getTypeName(type) {
      var ptr = ___getTypeName(type);
      var rv = readLatin1String(ptr);
      _free(ptr);
      return rv;
    }
    function throwUnboundTypeError(message, types) {
      var unboundTypes = [];
      var seen = {};
      function visit(type) {
        if (seen[type]) {
          return;
        }
        if (registeredTypes[type]) {
          return;
        }
        if (typeDependencies[type]) {
          typeDependencies[type].forEach(visit);
          return;
        }
        unboundTypes.push(type);
        seen[type] = true;
      }
      types.forEach(visit);
      throw new UnboundTypeError(message + ": " + unboundTypes.map(getTypeName).join([", "]));
    }
    function __embind_register_class(rawType, rawPointerType, rawConstPointerType, baseClassRawType, getActualTypeSignature, getActualType, upcastSignature, upcast, downcastSignature, downcast, name, destructorSignature, rawDestructor) {
      name = readLatin1String(name);
      getActualType = embind__requireFunction(getActualTypeSignature, getActualType);
      if (upcast) {
        upcast = embind__requireFunction(upcastSignature, upcast);
      }
      if (downcast) {
        downcast = embind__requireFunction(downcastSignature, downcast);
      }
      rawDestructor = embind__requireFunction(destructorSignature, rawDestructor);
      var legalFunctionName = makeLegalFunctionName(name);
      exposePublicSymbol(legalFunctionName, function() {
        throwUnboundTypeError("Cannot construct " + name + " due to unbound types", [baseClassRawType]);
      });
      whenDependentTypesAreResolved([rawType, rawPointerType, rawConstPointerType], baseClassRawType ? [baseClassRawType] : [], function(base) {
        base = base[0];
        var baseClass;
        var basePrototype;
        if (baseClassRawType) {
          baseClass = base.registeredClass;
          basePrototype = baseClass.instancePrototype;
        } else {
          basePrototype = ClassHandle.prototype;
        }
        var constructor = createNamedFunction(legalFunctionName, function() {
          if (Object.getPrototypeOf(this) !== instancePrototype) {
            throw new BindingError("Use 'new' to construct " + name);
          }
          if (registeredClass.constructor_body === undefined) {
            throw new BindingError(name + " has no accessible constructor");
          }
          var body = registeredClass.constructor_body[arguments.length];
          if (body === undefined) {
            throw new BindingError("Tried to invoke ctor of " + name + " with invalid number of parameters (" + arguments.length + ") - expected (" + Object.keys(registeredClass.constructor_body).toString() + ") parameters instead!");
          }
          return body.apply(this, arguments);
        });
        var instancePrototype = Object.create(basePrototype, { constructor: { value: constructor } });
        constructor.prototype = instancePrototype;
        var registeredClass = new RegisteredClass(name, constructor, instancePrototype, rawDestructor, baseClass, getActualType, upcast, downcast);
        var referenceConverter = new RegisteredPointer(name, registeredClass, true, false, false);
        var pointerConverter = new RegisteredPointer(name + "*", registeredClass, false, false, false);
        var constPointerConverter = new RegisteredPointer(name + " const*", registeredClass, false, true, false);
        registeredPointers[rawType] = { pointerType: pointerConverter, constPointerType: constPointerConverter };
        replacePublicSymbol(legalFunctionName, constructor);
        return [referenceConverter, pointerConverter, constPointerConverter];
      });
    }
    function new_(constructor, argumentList) {
      if (!(constructor instanceof Function)) {
        throw new TypeError("new_ called with constructor type " + typeof constructor + " which is not a function");
      }
      var dummy = createNamedFunction(constructor.name || "unknownFunctionName", function() {});
      dummy.prototype = constructor.prototype;
      var obj = new dummy;
      var r = constructor.apply(obj, argumentList);
      return r instanceof Object ? r : obj;
    }
    function runDestructors(destructors) {
      while (destructors.length) {
        var ptr = destructors.pop();
        var del = destructors.pop();
        del(ptr);
      }
    }
    function craftInvokerFunction(humanName, argTypes, classType, cppInvokerFunc, cppTargetFunc) {
      var argCount = argTypes.length;
      if (argCount < 2) {
        throwBindingError("argTypes array size mismatch! Must at least get return value and 'this' types!");
      }
      var isClassMethodFunc = argTypes[1] !== null && classType !== null;
      var needsDestructorStack = false;
      for (var i2 = 1;i2 < argTypes.length; ++i2) {
        if (argTypes[i2] !== null && argTypes[i2].destructorFunction === undefined) {
          needsDestructorStack = true;
          break;
        }
      }
      var returns = argTypes[0].name !== "void";
      var argsList = "";
      var argsListWired = "";
      for (var i2 = 0;i2 < argCount - 2; ++i2) {
        argsList += (i2 !== 0 ? ", " : "") + "arg" + i2;
        argsListWired += (i2 !== 0 ? ", " : "") + "arg" + i2 + "Wired";
      }
      var invokerFnBody = "return function " + makeLegalFunctionName(humanName) + "(" + argsList + `) {
` + "if (arguments.length !== " + (argCount - 2) + `) {
` + "throwBindingError('function " + humanName + " called with ' + arguments.length + ' arguments, expected " + (argCount - 2) + ` args!');
` + `}
`;
      if (needsDestructorStack) {
        invokerFnBody += `var destructors = [];
`;
      }
      var dtorStack = needsDestructorStack ? "destructors" : "null";
      var args1 = ["throwBindingError", "invoker", "fn", "runDestructors", "retType", "classParam"];
      var args2 = [throwBindingError, cppInvokerFunc, cppTargetFunc, runDestructors, argTypes[0], argTypes[1]];
      if (isClassMethodFunc) {
        invokerFnBody += "var thisWired = classParam.toWireType(" + dtorStack + `, this);
`;
      }
      for (var i2 = 0;i2 < argCount - 2; ++i2) {
        invokerFnBody += "var arg" + i2 + "Wired = argType" + i2 + ".toWireType(" + dtorStack + ", arg" + i2 + "); // " + argTypes[i2 + 2].name + `
`;
        args1.push("argType" + i2);
        args2.push(argTypes[i2 + 2]);
      }
      if (isClassMethodFunc) {
        argsListWired = "thisWired" + (argsListWired.length > 0 ? ", " : "") + argsListWired;
      }
      invokerFnBody += (returns ? "var rv = " : "") + "invoker(fn" + (argsListWired.length > 0 ? ", " : "") + argsListWired + `);
`;
      if (needsDestructorStack) {
        invokerFnBody += `runDestructors(destructors);
`;
      } else {
        for (var i2 = isClassMethodFunc ? 1 : 2;i2 < argTypes.length; ++i2) {
          var paramName = i2 === 1 ? "thisWired" : "arg" + (i2 - 2) + "Wired";
          if (argTypes[i2].destructorFunction !== null) {
            invokerFnBody += paramName + "_dtor(" + paramName + "); // " + argTypes[i2].name + `
`;
            args1.push(paramName + "_dtor");
            args2.push(argTypes[i2].destructorFunction);
          }
        }
      }
      if (returns) {
        invokerFnBody += `var ret = retType.fromWireType(rv);
` + `return ret;
`;
      } else {}
      invokerFnBody += `}
`;
      args1.push(invokerFnBody);
      var invokerFunction = new_(Function, args1).apply(null, args2);
      return invokerFunction;
    }
    function heap32VectorToArray(count, firstElement) {
      var array = [];
      for (var i2 = 0;i2 < count; i2++) {
        array.push(HEAP32[(firstElement >> 2) + i2]);
      }
      return array;
    }
    function __embind_register_class_class_function(rawClassType, methodName, argCount, rawArgTypesAddr, invokerSignature, rawInvoker, fn) {
      var rawArgTypes = heap32VectorToArray(argCount, rawArgTypesAddr);
      methodName = readLatin1String(methodName);
      rawInvoker = embind__requireFunction(invokerSignature, rawInvoker);
      whenDependentTypesAreResolved([], [rawClassType], function(classType) {
        classType = classType[0];
        var humanName = classType.name + "." + methodName;
        function unboundTypesHandler() {
          throwUnboundTypeError("Cannot call " + humanName + " due to unbound types", rawArgTypes);
        }
        if (methodName.startsWith("@@")) {
          methodName = Symbol[methodName.substring(2)];
        }
        var proto = classType.registeredClass.constructor;
        if (proto[methodName] === undefined) {
          unboundTypesHandler.argCount = argCount - 1;
          proto[methodName] = unboundTypesHandler;
        } else {
          ensureOverloadTable(proto, methodName, humanName);
          proto[methodName].overloadTable[argCount - 1] = unboundTypesHandler;
        }
        whenDependentTypesAreResolved([], rawArgTypes, function(argTypes) {
          var invokerArgsArray = [argTypes[0], null].concat(argTypes.slice(1));
          var func = craftInvokerFunction(humanName, invokerArgsArray, null, rawInvoker, fn);
          if (proto[methodName].overloadTable === undefined) {
            func.argCount = argCount - 1;
            proto[methodName] = func;
          } else {
            proto[methodName].overloadTable[argCount - 1] = func;
          }
          return [];
        });
        return [];
      });
    }
    function __embind_register_class_constructor(rawClassType, argCount, rawArgTypesAddr, invokerSignature, invoker, rawConstructor) {
      assert(argCount > 0);
      var rawArgTypes = heap32VectorToArray(argCount, rawArgTypesAddr);
      invoker = embind__requireFunction(invokerSignature, invoker);
      whenDependentTypesAreResolved([], [rawClassType], function(classType) {
        classType = classType[0];
        var humanName = "constructor " + classType.name;
        if (classType.registeredClass.constructor_body === undefined) {
          classType.registeredClass.constructor_body = [];
        }
        if (classType.registeredClass.constructor_body[argCount - 1] !== undefined) {
          throw new BindingError("Cannot register multiple constructors with identical number of parameters (" + (argCount - 1) + ") for class '" + classType.name + "'! Overload resolution is currently only performed using the parameter count, not actual type info!");
        }
        classType.registeredClass.constructor_body[argCount - 1] = () => {
          throwUnboundTypeError("Cannot construct " + classType.name + " due to unbound types", rawArgTypes);
        };
        whenDependentTypesAreResolved([], rawArgTypes, function(argTypes) {
          argTypes.splice(1, 0, null);
          classType.registeredClass.constructor_body[argCount - 1] = craftInvokerFunction(humanName, argTypes, null, invoker, rawConstructor);
          return [];
        });
        return [];
      });
    }
    function __embind_register_class_function(rawClassType, methodName, argCount, rawArgTypesAddr, invokerSignature, rawInvoker, context, isPureVirtual) {
      var rawArgTypes = heap32VectorToArray(argCount, rawArgTypesAddr);
      methodName = readLatin1String(methodName);
      rawInvoker = embind__requireFunction(invokerSignature, rawInvoker);
      whenDependentTypesAreResolved([], [rawClassType], function(classType) {
        classType = classType[0];
        var humanName = classType.name + "." + methodName;
        if (methodName.startsWith("@@")) {
          methodName = Symbol[methodName.substring(2)];
        }
        if (isPureVirtual) {
          classType.registeredClass.pureVirtualFunctions.push(methodName);
        }
        function unboundTypesHandler() {
          throwUnboundTypeError("Cannot call " + humanName + " due to unbound types", rawArgTypes);
        }
        var proto = classType.registeredClass.instancePrototype;
        var method = proto[methodName];
        if (method === undefined || method.overloadTable === undefined && method.className !== classType.name && method.argCount === argCount - 2) {
          unboundTypesHandler.argCount = argCount - 2;
          unboundTypesHandler.className = classType.name;
          proto[methodName] = unboundTypesHandler;
        } else {
          ensureOverloadTable(proto, methodName, humanName);
          proto[methodName].overloadTable[argCount - 2] = unboundTypesHandler;
        }
        whenDependentTypesAreResolved([], rawArgTypes, function(argTypes) {
          var memberFunction = craftInvokerFunction(humanName, argTypes, classType, rawInvoker, context);
          if (proto[methodName].overloadTable === undefined) {
            memberFunction.argCount = argCount - 2;
            proto[methodName] = memberFunction;
          } else {
            proto[methodName].overloadTable[argCount - 2] = memberFunction;
          }
          return [];
        });
        return [];
      });
    }
    var emval_free_list = [];
    var emval_handle_array = [{}, { value: undefined }, { value: null }, { value: true }, { value: false }];
    function __emval_decref(handle) {
      if (handle > 4 && --emval_handle_array[handle].refcount === 0) {
        emval_handle_array[handle] = undefined;
        emval_free_list.push(handle);
      }
    }
    function count_emval_handles() {
      var count = 0;
      for (var i2 = 5;i2 < emval_handle_array.length; ++i2) {
        if (emval_handle_array[i2] !== undefined) {
          ++count;
        }
      }
      return count;
    }
    function get_first_emval() {
      for (var i2 = 5;i2 < emval_handle_array.length; ++i2) {
        if (emval_handle_array[i2] !== undefined) {
          return emval_handle_array[i2];
        }
      }
      return null;
    }
    function init_emval() {
      Module2["count_emval_handles"] = count_emval_handles;
      Module2["get_first_emval"] = get_first_emval;
    }
    var Emval = { toValue: (handle) => {
      if (!handle) {
        throwBindingError("Cannot use deleted val. handle = " + handle);
      }
      return emval_handle_array[handle].value;
    }, toHandle: (value) => {
      switch (value) {
        case undefined:
          return 1;
        case null:
          return 2;
        case true:
          return 3;
        case false:
          return 4;
        default: {
          var handle = emval_free_list.length ? emval_free_list.pop() : emval_handle_array.length;
          emval_handle_array[handle] = { refcount: 1, value };
          return handle;
        }
      }
    } };
    function __embind_register_emval(rawType, name) {
      name = readLatin1String(name);
      registerType(rawType, { name, fromWireType: function(handle) {
        var rv = Emval.toValue(handle);
        __emval_decref(handle);
        return rv;
      }, toWireType: function(destructors, value) {
        return Emval.toHandle(value);
      }, argPackAdvance: 8, readValueFromPointer: simpleReadValueFromPointer, destructorFunction: null });
    }
    function enumReadValueFromPointer(name, shift, signed) {
      switch (shift) {
        case 0:
          return function(pointer) {
            var heap = signed ? HEAP8 : HEAPU8;
            return this["fromWireType"](heap[pointer]);
          };
        case 1:
          return function(pointer) {
            var heap = signed ? HEAP16 : HEAPU16;
            return this["fromWireType"](heap[pointer >> 1]);
          };
        case 2:
          return function(pointer) {
            var heap = signed ? HEAP32 : HEAPU32;
            return this["fromWireType"](heap[pointer >> 2]);
          };
        default:
          throw new TypeError("Unknown integer type: " + name);
      }
    }
    function __embind_register_enum(rawType, name, size, isSigned) {
      var shift = getShiftFromSize(size);
      name = readLatin1String(name);
      function ctor() {}
      ctor.values = {};
      registerType(rawType, { name, constructor: ctor, fromWireType: function(c) {
        return this.constructor.values[c];
      }, toWireType: function(destructors, c) {
        return c.value;
      }, argPackAdvance: 8, readValueFromPointer: enumReadValueFromPointer(name, shift, isSigned), destructorFunction: null });
      exposePublicSymbol(name, ctor);
    }
    function requireRegisteredType(rawType, humanName) {
      var impl = registeredTypes[rawType];
      if (impl === undefined) {
        throwBindingError(humanName + " has unknown type " + getTypeName(rawType));
      }
      return impl;
    }
    function __embind_register_enum_value(rawEnumType, name, enumValue) {
      var enumType = requireRegisteredType(rawEnumType, "enum");
      name = readLatin1String(name);
      var Enum = enumType.constructor;
      var Value = Object.create(enumType.constructor.prototype, { value: { value: enumValue }, constructor: { value: createNamedFunction(enumType.name + "_" + name, function() {}) } });
      Enum.values[enumValue] = Value;
      Enum[name] = Value;
    }
    function _embind_repr(v) {
      if (v === null) {
        return "null";
      }
      var t = typeof v;
      if (t === "object" || t === "array" || t === "function") {
        return v.toString();
      } else {
        return "" + v;
      }
    }
    function floatReadValueFromPointer(name, shift) {
      switch (shift) {
        case 2:
          return function(pointer) {
            return this["fromWireType"](HEAPF32[pointer >> 2]);
          };
        case 3:
          return function(pointer) {
            return this["fromWireType"](HEAPF64[pointer >> 3]);
          };
        default:
          throw new TypeError("Unknown float type: " + name);
      }
    }
    function __embind_register_float(rawType, name, size) {
      var shift = getShiftFromSize(size);
      name = readLatin1String(name);
      registerType(rawType, { name, fromWireType: function(value) {
        return value;
      }, toWireType: function(destructors, value) {
        return value;
      }, argPackAdvance: 8, readValueFromPointer: floatReadValueFromPointer(name, shift), destructorFunction: null });
    }
    function integerReadValueFromPointer(name, shift, signed) {
      switch (shift) {
        case 0:
          return signed ? function readS8FromPointer(pointer) {
            return HEAP8[pointer];
          } : function readU8FromPointer(pointer) {
            return HEAPU8[pointer];
          };
        case 1:
          return signed ? function readS16FromPointer(pointer) {
            return HEAP16[pointer >> 1];
          } : function readU16FromPointer(pointer) {
            return HEAPU16[pointer >> 1];
          };
        case 2:
          return signed ? function readS32FromPointer(pointer) {
            return HEAP32[pointer >> 2];
          } : function readU32FromPointer(pointer) {
            return HEAPU32[pointer >> 2];
          };
        default:
          throw new TypeError("Unknown integer type: " + name);
      }
    }
    function __embind_register_integer(primitiveType, name, size, minRange, maxRange) {
      name = readLatin1String(name);
      if (maxRange === -1) {
        maxRange = 4294967295;
      }
      var shift = getShiftFromSize(size);
      var fromWireType = (value) => value;
      if (minRange === 0) {
        var bitshift = 32 - 8 * size;
        fromWireType = (value) => value << bitshift >>> bitshift;
      }
      var isUnsignedType = name.includes("unsigned");
      var checkAssertions = (value, toTypeName) => {};
      var toWireType;
      if (isUnsignedType) {
        toWireType = function(destructors, value) {
          checkAssertions(value, this.name);
          return value >>> 0;
        };
      } else {
        toWireType = function(destructors, value) {
          checkAssertions(value, this.name);
          return value;
        };
      }
      registerType(primitiveType, { name, fromWireType, toWireType, argPackAdvance: 8, readValueFromPointer: integerReadValueFromPointer(name, shift, minRange !== 0), destructorFunction: null });
    }
    function __embind_register_memory_view(rawType, dataTypeIndex, name) {
      var typeMapping = [Int8Array, Uint8Array, Int16Array, Uint16Array, Int32Array, Uint32Array, Float32Array, Float64Array];
      var TA = typeMapping[dataTypeIndex];
      function decodeMemoryView(handle) {
        handle = handle >> 2;
        var heap = HEAPU32;
        var size = heap[handle];
        var data = heap[handle + 1];
        return new TA(buffer, data, size);
      }
      name = readLatin1String(name);
      registerType(rawType, { name, fromWireType: decodeMemoryView, argPackAdvance: 8, readValueFromPointer: decodeMemoryView }, { ignoreDuplicateRegistrations: true });
    }
    function __embind_register_std_string(rawType, name) {
      name = readLatin1String(name);
      var stdStringIsUTF8 = name === "std::string";
      registerType(rawType, { name, fromWireType: function(value) {
        var length = HEAPU32[value >> 2];
        var str;
        if (stdStringIsUTF8) {
          var decodeStartPtr = value + 4;
          for (var i2 = 0;i2 <= length; ++i2) {
            var currentBytePtr = value + 4 + i2;
            if (i2 == length || HEAPU8[currentBytePtr] == 0) {
              var maxRead = currentBytePtr - decodeStartPtr;
              var stringSegment = UTF8ToString(decodeStartPtr, maxRead);
              if (str === undefined) {
                str = stringSegment;
              } else {
                str += String.fromCharCode(0);
                str += stringSegment;
              }
              decodeStartPtr = currentBytePtr + 1;
            }
          }
        } else {
          var a = new Array(length);
          for (var i2 = 0;i2 < length; ++i2) {
            a[i2] = String.fromCharCode(HEAPU8[value + 4 + i2]);
          }
          str = a.join("");
        }
        _free(value);
        return str;
      }, toWireType: function(destructors, value) {
        if (value instanceof ArrayBuffer) {
          value = new Uint8Array(value);
        }
        var getLength;
        var valueIsOfTypeString = typeof value == "string";
        if (!(valueIsOfTypeString || value instanceof Uint8Array || value instanceof Uint8ClampedArray || value instanceof Int8Array)) {
          throwBindingError("Cannot pass non-string to std::string");
        }
        if (stdStringIsUTF8 && valueIsOfTypeString) {
          getLength = () => lengthBytesUTF8(value);
        } else {
          getLength = () => value.length;
        }
        var length = getLength();
        var ptr = _malloc(4 + length + 1);
        HEAPU32[ptr >> 2] = length;
        if (stdStringIsUTF8 && valueIsOfTypeString) {
          stringToUTF8(value, ptr + 4, length + 1);
        } else {
          if (valueIsOfTypeString) {
            for (var i2 = 0;i2 < length; ++i2) {
              var charCode = value.charCodeAt(i2);
              if (charCode > 255) {
                _free(ptr);
                throwBindingError("String has UTF-16 code units that do not fit in 8 bits");
              }
              HEAPU8[ptr + 4 + i2] = charCode;
            }
          } else {
            for (var i2 = 0;i2 < length; ++i2) {
              HEAPU8[ptr + 4 + i2] = value[i2];
            }
          }
        }
        if (destructors !== null) {
          destructors.push(_free, ptr);
        }
        return ptr;
      }, argPackAdvance: 8, readValueFromPointer: simpleReadValueFromPointer, destructorFunction: function(ptr) {
        _free(ptr);
      } });
    }
    function __embind_register_std_wstring(rawType, charSize, name) {
      name = readLatin1String(name);
      var decodeString, encodeString, getHeap, lengthBytesUTF, shift;
      if (charSize === 2) {
        decodeString = UTF16ToString;
        encodeString = stringToUTF16;
        lengthBytesUTF = lengthBytesUTF16;
        getHeap = () => HEAPU16;
        shift = 1;
      } else if (charSize === 4) {
        decodeString = UTF32ToString;
        encodeString = stringToUTF32;
        lengthBytesUTF = lengthBytesUTF32;
        getHeap = () => HEAPU32;
        shift = 2;
      }
      registerType(rawType, { name, fromWireType: function(value) {
        var length = HEAPU32[value >> 2];
        var HEAP = getHeap();
        var str;
        var decodeStartPtr = value + 4;
        for (var i2 = 0;i2 <= length; ++i2) {
          var currentBytePtr = value + 4 + i2 * charSize;
          if (i2 == length || HEAP[currentBytePtr >> shift] == 0) {
            var maxReadBytes = currentBytePtr - decodeStartPtr;
            var stringSegment = decodeString(decodeStartPtr, maxReadBytes);
            if (str === undefined) {
              str = stringSegment;
            } else {
              str += String.fromCharCode(0);
              str += stringSegment;
            }
            decodeStartPtr = currentBytePtr + charSize;
          }
        }
        _free(value);
        return str;
      }, toWireType: function(destructors, value) {
        if (!(typeof value == "string")) {
          throwBindingError("Cannot pass non-string to C++ string type " + name);
        }
        var length = lengthBytesUTF(value);
        var ptr = _malloc(4 + length + charSize);
        HEAPU32[ptr >> 2] = length >> shift;
        encodeString(value, ptr + 4, length + charSize);
        if (destructors !== null) {
          destructors.push(_free, ptr);
        }
        return ptr;
      }, argPackAdvance: 8, readValueFromPointer: simpleReadValueFromPointer, destructorFunction: function(ptr) {
        _free(ptr);
      } });
    }
    function __embind_register_void(rawType, name) {
      name = readLatin1String(name);
      registerType(rawType, { isVoid: true, name, argPackAdvance: 0, fromWireType: function() {
        return;
      }, toWireType: function(destructors, o) {
        return;
      } });
    }
    function __emscripten_date_now() {
      return Date.now();
    }
    var nowIsMonotonic = true;
    function __emscripten_get_now_is_monotonic() {
      return nowIsMonotonic;
    }
    function __emval_as(handle, returnType, destructorsRef) {
      handle = Emval.toValue(handle);
      returnType = requireRegisteredType(returnType, "emval::as");
      var destructors = [];
      var rd = Emval.toHandle(destructors);
      HEAP32[destructorsRef >> 2] = rd;
      return returnType["toWireType"](destructors, handle);
    }
    function emval_allocateDestructors(destructorsRef) {
      var destructors = [];
      HEAP32[destructorsRef >> 2] = Emval.toHandle(destructors);
      return destructors;
    }
    var emval_symbols = {};
    function getStringOrSymbol(address) {
      var symbol = emval_symbols[address];
      if (symbol === undefined) {
        return readLatin1String(address);
      }
      return symbol;
    }
    var emval_methodCallers = [];
    function __emval_call_method(caller, handle, methodName, destructorsRef, args) {
      caller = emval_methodCallers[caller];
      handle = Emval.toValue(handle);
      methodName = getStringOrSymbol(methodName);
      return caller(handle, methodName, emval_allocateDestructors(destructorsRef), args);
    }
    function emval_get_global() {
      if (typeof globalThis == "object") {
        return globalThis;
      }
      return function() {
        return Function;
      }()("return this")();
    }
    function __emval_get_global(name) {
      if (name === 0) {
        return Emval.toHandle(emval_get_global());
      } else {
        name = getStringOrSymbol(name);
        return Emval.toHandle(emval_get_global()[name]);
      }
    }
    function emval_addMethodCaller(caller) {
      var id = emval_methodCallers.length;
      emval_methodCallers.push(caller);
      return id;
    }
    function emval_lookupTypes(argCount, argTypes) {
      var a = new Array(argCount);
      for (var i2 = 0;i2 < argCount; ++i2) {
        a[i2] = requireRegisteredType(HEAPU32[argTypes + i2 * POINTER_SIZE >> 2], "parameter " + i2);
      }
      return a;
    }
    var emval_registeredMethods = [];
    function __emval_get_method_caller(argCount, argTypes) {
      var types = emval_lookupTypes(argCount, argTypes);
      var retType = types[0];
      var signatureName = retType.name + "_$" + types.slice(1).map(function(t) {
        return t.name;
      }).join("_") + "$";
      var returnId = emval_registeredMethods[signatureName];
      if (returnId !== undefined) {
        return returnId;
      }
      var params = ["retType"];
      var args = [retType];
      var argsList = "";
      for (var i2 = 0;i2 < argCount - 1; ++i2) {
        argsList += (i2 !== 0 ? ", " : "") + "arg" + i2;
        params.push("argType" + i2);
        args.push(types[1 + i2]);
      }
      var functionName = makeLegalFunctionName("methodCaller_" + signatureName);
      var functionBody = "return function " + functionName + `(handle, name, destructors, args) {
`;
      var offset = 0;
      for (var i2 = 0;i2 < argCount - 1; ++i2) {
        functionBody += "    var arg" + i2 + " = argType" + i2 + ".readValueFromPointer(args" + (offset ? "+" + offset : "") + `);
`;
        offset += types[i2 + 1]["argPackAdvance"];
      }
      functionBody += "    var rv = handle[name](" + argsList + `);
`;
      for (var i2 = 0;i2 < argCount - 1; ++i2) {
        if (types[i2 + 1]["deleteObject"]) {
          functionBody += "    argType" + i2 + ".deleteObject(arg" + i2 + `);
`;
        }
      }
      if (!retType.isVoid) {
        functionBody += `    return retType.toWireType(destructors, rv);
`;
      }
      functionBody += `};
`;
      params.push(functionBody);
      var invokerFunction = new_(Function, params).apply(null, args);
      returnId = emval_addMethodCaller(invokerFunction);
      emval_registeredMethods[signatureName] = returnId;
      return returnId;
    }
    function __emval_get_property(handle, key) {
      handle = Emval.toValue(handle);
      key = Emval.toValue(key);
      return Emval.toHandle(handle[key]);
    }
    function __emval_incref(handle) {
      if (handle > 4) {
        emval_handle_array[handle].refcount += 1;
      }
    }
    function __emval_new_cstring(v) {
      return Emval.toHandle(getStringOrSymbol(v));
    }
    function __emval_run_destructors(handle) {
      var destructors = Emval.toValue(handle);
      runDestructors(destructors);
      __emval_decref(handle);
    }
    function __emval_set_property(handle, key, value) {
      handle = Emval.toValue(handle);
      key = Emval.toValue(key);
      value = Emval.toValue(value);
      handle[key] = value;
    }
    function __emval_take_value(type, argv) {
      type = requireRegisteredType(type, "_emval_take_value");
      var v = type["readValueFromPointer"](argv);
      return Emval.toHandle(v);
    }
    function __emval_typeof(handle) {
      handle = Emval.toValue(handle);
      return Emval.toHandle(typeof handle);
    }
    function __localtime_js(time, tmPtr) {
      var date = new Date(HEAP32[time >> 2] * 1000);
      HEAP32[tmPtr >> 2] = date.getSeconds();
      HEAP32[tmPtr + 4 >> 2] = date.getMinutes();
      HEAP32[tmPtr + 8 >> 2] = date.getHours();
      HEAP32[tmPtr + 12 >> 2] = date.getDate();
      HEAP32[tmPtr + 16 >> 2] = date.getMonth();
      HEAP32[tmPtr + 20 >> 2] = date.getFullYear() - 1900;
      HEAP32[tmPtr + 24 >> 2] = date.getDay();
      var start = new Date(date.getFullYear(), 0, 1);
      var yday = (date.getTime() - start.getTime()) / (1000 * 60 * 60 * 24) | 0;
      HEAP32[tmPtr + 28 >> 2] = yday;
      HEAP32[tmPtr + 36 >> 2] = -(date.getTimezoneOffset() * 60);
      var summerOffset = new Date(date.getFullYear(), 6, 1).getTimezoneOffset();
      var winterOffset = start.getTimezoneOffset();
      var dst = (summerOffset != winterOffset && date.getTimezoneOffset() == Math.min(winterOffset, summerOffset)) | 0;
      HEAP32[tmPtr + 32 >> 2] = dst;
    }
    function __munmap_js(addr, len, prot, flags, fd, offset) {
      try {
        var stream = FS.getStream(fd);
        if (stream) {
          if (prot & 2) {
            SYSCALLS.doMsync(addr, stream, len, flags, offset);
          }
          FS.munmap(stream);
        }
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return -e.errno;
      }
    }
    function _tzset_impl(timezone, daylight, tzname) {
      var currentYear = new Date().getFullYear();
      var winter = new Date(currentYear, 0, 1);
      var summer = new Date(currentYear, 6, 1);
      var winterOffset = winter.getTimezoneOffset();
      var summerOffset = summer.getTimezoneOffset();
      var stdTimezoneOffset = Math.max(winterOffset, summerOffset);
      HEAP32[timezone >> 2] = stdTimezoneOffset * 60;
      HEAP32[daylight >> 2] = Number(winterOffset != summerOffset);
      function extractZone(date) {
        var match = date.toTimeString().match(/\(([A-Za-z ]+)\)$/);
        return match ? match[1] : "GMT";
      }
      var winterName = extractZone(winter);
      var summerName = extractZone(summer);
      var winterNamePtr = allocateUTF8(winterName);
      var summerNamePtr = allocateUTF8(summerName);
      if (summerOffset < winterOffset) {
        HEAPU32[tzname >> 2] = winterNamePtr;
        HEAPU32[tzname + 4 >> 2] = summerNamePtr;
      } else {
        HEAPU32[tzname >> 2] = summerNamePtr;
        HEAPU32[tzname + 4 >> 2] = winterNamePtr;
      }
    }
    function __tzset_js(timezone, daylight, tzname) {
      if (__tzset_js.called)
        return;
      __tzset_js.called = true;
      _tzset_impl(timezone, daylight, tzname);
    }
    function _abort() {
      abort("");
    }
    function getHeapMax() {
      return 2147483648;
    }
    function _emscripten_get_heap_max() {
      return getHeapMax();
    }
    var _emscripten_get_now;
    if (ENVIRONMENT_IS_NODE) {
      _emscripten_get_now = () => {
        var t = process["hrtime"]();
        return t[0] * 1000 + t[1] / 1e6;
      };
    } else
      _emscripten_get_now = () => performance.now();
    function _emscripten_memcpy_big(dest, src, num) {
      HEAPU8.copyWithin(dest, src, src + num);
    }
    function emscripten_realloc_buffer(size) {
      try {
        wasmMemory.grow(size - buffer.byteLength + 65535 >>> 16);
        updateGlobalBufferAndViews(wasmMemory.buffer);
        return 1;
      } catch (e) {}
    }
    function _emscripten_resize_heap(requestedSize) {
      var oldSize = HEAPU8.length;
      requestedSize = requestedSize >>> 0;
      var maxHeapSize = getHeapMax();
      if (requestedSize > maxHeapSize) {
        return false;
      }
      let alignUp = (x, multiple) => x + (multiple - x % multiple) % multiple;
      for (var cutDown = 1;cutDown <= 4; cutDown *= 2) {
        var overGrownHeapSize = oldSize * (1 + 0.2 / cutDown);
        overGrownHeapSize = Math.min(overGrownHeapSize, requestedSize + 100663296);
        var newSize = Math.min(maxHeapSize, alignUp(Math.max(requestedSize, overGrownHeapSize), 65536));
        var replacement = emscripten_realloc_buffer(newSize);
        if (replacement) {
          return true;
        }
      }
      return false;
    }
    var ENV = {};
    function getExecutableName() {
      return thisProgram || "./this.program";
    }
    function getEnvStrings() {
      if (!getEnvStrings.strings) {
        var lang = (typeof navigator == "object" && navigator.languages && navigator.languages[0] || "C").replace("-", "_") + ".UTF-8";
        var env = { USER: "web_user", LOGNAME: "web_user", PATH: "/", PWD: "/", HOME: "/home/web_user", LANG: lang, _: getExecutableName() };
        for (var x in ENV) {
          if (ENV[x] === undefined)
            delete env[x];
          else
            env[x] = ENV[x];
        }
        var strings = [];
        for (var x in env) {
          strings.push(x + "=" + env[x]);
        }
        getEnvStrings.strings = strings;
      }
      return getEnvStrings.strings;
    }
    function _environ_get(__environ, environ_buf) {
      var bufSize = 0;
      getEnvStrings().forEach(function(string, i2) {
        var ptr = environ_buf + bufSize;
        HEAPU32[__environ + i2 * 4 >> 2] = ptr;
        writeAsciiToMemory(string, ptr);
        bufSize += string.length + 1;
      });
      return 0;
    }
    function _environ_sizes_get(penviron_count, penviron_buf_size) {
      var strings = getEnvStrings();
      HEAPU32[penviron_count >> 2] = strings.length;
      var bufSize = 0;
      strings.forEach(function(string) {
        bufSize += string.length + 1;
      });
      HEAPU32[penviron_buf_size >> 2] = bufSize;
      return 0;
    }
    function _exit(status) {
      exit(status);
    }
    function _fd_close(fd) {
      try {
        var stream = SYSCALLS.getStreamFromFD(fd);
        FS.close(stream);
        return 0;
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return e.errno;
      }
    }
    function doReadv(stream, iov, iovcnt, offset) {
      var ret = 0;
      for (var i2 = 0;i2 < iovcnt; i2++) {
        var ptr = HEAPU32[iov >> 2];
        var len = HEAPU32[iov + 4 >> 2];
        iov += 8;
        var curr = FS.read(stream, HEAP8, ptr, len, offset);
        if (curr < 0)
          return -1;
        ret += curr;
        if (curr < len)
          break;
      }
      return ret;
    }
    function _fd_read(fd, iov, iovcnt, pnum) {
      try {
        var stream = SYSCALLS.getStreamFromFD(fd);
        var num = doReadv(stream, iov, iovcnt);
        HEAP32[pnum >> 2] = num;
        return 0;
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return e.errno;
      }
    }
    function convertI32PairToI53Checked(lo, hi) {
      return hi + 2097152 >>> 0 < 4194305 - !!lo ? (lo >>> 0) + hi * 4294967296 : NaN;
    }
    function _fd_seek(fd, offset_low, offset_high, whence, newOffset) {
      try {
        var offset = convertI32PairToI53Checked(offset_low, offset_high);
        if (isNaN(offset))
          return 61;
        var stream = SYSCALLS.getStreamFromFD(fd);
        FS.llseek(stream, offset, whence);
        tempI64 = [stream.position >>> 0, (tempDouble = stream.position, +Math.abs(tempDouble) >= 1 ? tempDouble > 0 ? (Math.min(+Math.floor(tempDouble / 4294967296), 4294967295) | 0) >>> 0 : ~~+Math.ceil((tempDouble - +(~~tempDouble >>> 0)) / 4294967296) >>> 0 : 0)], HEAP32[newOffset >> 2] = tempI64[0], HEAP32[newOffset + 4 >> 2] = tempI64[1];
        if (stream.getdents && offset === 0 && whence === 0)
          stream.getdents = null;
        return 0;
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return e.errno;
      }
    }
    function doWritev(stream, iov, iovcnt, offset) {
      var ret = 0;
      for (var i2 = 0;i2 < iovcnt; i2++) {
        var ptr = HEAPU32[iov >> 2];
        var len = HEAPU32[iov + 4 >> 2];
        iov += 8;
        var curr = FS.write(stream, HEAP8, ptr, len, offset);
        if (curr < 0)
          return -1;
        ret += curr;
      }
      return ret;
    }
    function _fd_write(fd, iov, iovcnt, pnum) {
      try {
        var stream = SYSCALLS.getStreamFromFD(fd);
        var num = doWritev(stream, iov, iovcnt);
        HEAPU32[pnum >> 2] = num;
        return 0;
      } catch (e) {
        if (typeof FS == "undefined" || !(e instanceof FS.ErrnoError))
          throw e;
        return e.errno;
      }
    }
    function inetPton4(str) {
      var b = str.split(".");
      for (var i2 = 0;i2 < 4; i2++) {
        var tmp = Number(b[i2]);
        if (isNaN(tmp))
          return null;
        b[i2] = tmp;
      }
      return (b[0] | b[1] << 8 | b[2] << 16 | b[3] << 24) >>> 0;
    }
    function jstoi_q(str) {
      return parseInt(str);
    }
    function inetPton6(str) {
      var words;
      var w, offset, z;
      var valid6regx = /^((?=.*::)(?!.*::.+::)(::)?([\dA-F]{1,4}:(:|\b)|){5}|([\dA-F]{1,4}:){6})((([\dA-F]{1,4}((?!\3)::|:\b|$))|(?!\2\3)){2}|(((2[0-4]|1\d|[1-9])?\d|25[0-5])\.?\b){4})$/i;
      var parts = [];
      if (!valid6regx.test(str)) {
        return null;
      }
      if (str === "::") {
        return [0, 0, 0, 0, 0, 0, 0, 0];
      }
      if (str.startsWith("::")) {
        str = str.replace("::", "Z:");
      } else {
        str = str.replace("::", ":Z:");
      }
      if (str.indexOf(".") > 0) {
        str = str.replace(new RegExp("[.]", "g"), ":");
        words = str.split(":");
        words[words.length - 4] = jstoi_q(words[words.length - 4]) + jstoi_q(words[words.length - 3]) * 256;
        words[words.length - 3] = jstoi_q(words[words.length - 2]) + jstoi_q(words[words.length - 1]) * 256;
        words = words.slice(0, words.length - 2);
      } else {
        words = str.split(":");
      }
      offset = 0;
      z = 0;
      for (w = 0;w < words.length; w++) {
        if (typeof words[w] == "string") {
          if (words[w] === "Z") {
            for (z = 0;z < 8 - words.length + 1; z++) {
              parts[w + z] = 0;
            }
            offset = z - 1;
          } else {
            parts[w + offset] = _htons(parseInt(words[w], 16));
          }
        } else {
          parts[w + offset] = words[w];
        }
      }
      return [parts[1] << 16 | parts[0], parts[3] << 16 | parts[2], parts[5] << 16 | parts[4], parts[7] << 16 | parts[6]];
    }
    var DNS = { address_map: { id: 1, addrs: {}, names: {} }, lookup_name: function(name) {
      var res = inetPton4(name);
      if (res !== null) {
        return name;
      }
      res = inetPton6(name);
      if (res !== null) {
        return name;
      }
      var addr;
      if (DNS.address_map.addrs[name]) {
        addr = DNS.address_map.addrs[name];
      } else {
        var id = DNS.address_map.id++;
        assert(id < 65535, "exceeded max address mappings of 65535");
        addr = "172.29." + (id & 255) + "." + (id & 65280);
        DNS.address_map.names[addr] = name;
        DNS.address_map.addrs[name] = addr;
      }
      return addr;
    }, lookup_addr: function(addr) {
      if (DNS.address_map.names[addr]) {
        return DNS.address_map.names[addr];
      }
      return null;
    } };
    function getHostByName(name) {
      var ret = _malloc(20);
      var nameBuf = _malloc(name.length + 1);
      stringToUTF8(name, nameBuf, name.length + 1);
      HEAPU32[ret >> 2] = nameBuf;
      var aliasesBuf = _malloc(4);
      HEAPU32[aliasesBuf >> 2] = 0;
      HEAPU32[ret + 4 >> 2] = aliasesBuf;
      var afinet = 2;
      HEAP32[ret + 8 >> 2] = afinet;
      HEAP32[ret + 12 >> 2] = 4;
      var addrListBuf = _malloc(12);
      HEAPU32[addrListBuf >> 2] = addrListBuf + 8;
      HEAPU32[addrListBuf + 4 >> 2] = 0;
      HEAP32[addrListBuf + 8 >> 2] = inetPton4(DNS.lookup_name(name));
      HEAPU32[ret + 16 >> 2] = addrListBuf;
      return ret;
    }
    function _gethostbyname(name) {
      return getHostByName(UTF8ToString(name));
    }
    function __webgl_enable_ANGLE_instanced_arrays(ctx) {
      var ext = ctx.getExtension("ANGLE_instanced_arrays");
      if (ext) {
        ctx["vertexAttribDivisor"] = function(index, divisor) {
          ext["vertexAttribDivisorANGLE"](index, divisor);
        };
        ctx["drawArraysInstanced"] = function(mode, first, count, primcount) {
          ext["drawArraysInstancedANGLE"](mode, first, count, primcount);
        };
        ctx["drawElementsInstanced"] = function(mode, count, type, indices, primcount) {
          ext["drawElementsInstancedANGLE"](mode, count, type, indices, primcount);
        };
        return 1;
      }
    }
    function __webgl_enable_OES_vertex_array_object(ctx) {
      var ext = ctx.getExtension("OES_vertex_array_object");
      if (ext) {
        ctx["createVertexArray"] = function() {
          return ext["createVertexArrayOES"]();
        };
        ctx["deleteVertexArray"] = function(vao) {
          ext["deleteVertexArrayOES"](vao);
        };
        ctx["bindVertexArray"] = function(vao) {
          ext["bindVertexArrayOES"](vao);
        };
        ctx["isVertexArray"] = function(vao) {
          return ext["isVertexArrayOES"](vao);
        };
        return 1;
      }
    }
    function __webgl_enable_WEBGL_draw_buffers(ctx) {
      var ext = ctx.getExtension("WEBGL_draw_buffers");
      if (ext) {
        ctx["drawBuffers"] = function(n, bufs) {
          ext["drawBuffersWEBGL"](n, bufs);
        };
        return 1;
      }
    }
    function __webgl_enable_WEBGL_multi_draw(ctx) {
      return !!(ctx.multiDrawWebgl = ctx.getExtension("WEBGL_multi_draw"));
    }
    var GL = { counter: 1, buffers: [], programs: [], framebuffers: [], renderbuffers: [], textures: [], shaders: [], vaos: [], contexts: [], offscreenCanvases: {}, queries: [], stringCache: {}, unpackAlignment: 4, recordError: function recordError(errorCode) {
      if (!GL.lastError) {
        GL.lastError = errorCode;
      }
    }, getNewId: function(table) {
      var ret = GL.counter++;
      for (var i2 = table.length;i2 < ret; i2++) {
        table[i2] = null;
      }
      return ret;
    }, getSource: function(shader, count, string, length) {
      var source = "";
      for (var i2 = 0;i2 < count; ++i2) {
        var len = length ? HEAP32[length + i2 * 4 >> 2] : -1;
        source += UTF8ToString(HEAP32[string + i2 * 4 >> 2], len < 0 ? undefined : len);
      }
      return source;
    }, createContext: function(canvas, webGLContextAttributes) {
      if (!canvas.getContextSafariWebGL2Fixed) {
        let fixedGetContext = function(ver, attrs) {
          var gl = canvas.getContextSafariWebGL2Fixed(ver, attrs);
          return ver == "webgl" == gl instanceof WebGLRenderingContext ? gl : null;
        };
        canvas.getContextSafariWebGL2Fixed = canvas.getContext;
        canvas.getContext = fixedGetContext;
      }
      var ctx = canvas.getContext("webgl", webGLContextAttributes);
      if (!ctx)
        return 0;
      var handle = GL.registerContext(ctx, webGLContextAttributes);
      return handle;
    }, registerContext: function(ctx, webGLContextAttributes) {
      var handle = GL.getNewId(GL.contexts);
      var context = { handle, attributes: webGLContextAttributes, version: webGLContextAttributes.majorVersion, GLctx: ctx };
      if (ctx.canvas)
        ctx.canvas.GLctxObject = context;
      GL.contexts[handle] = context;
      if (typeof webGLContextAttributes.enableExtensionsByDefault == "undefined" || webGLContextAttributes.enableExtensionsByDefault) {
        GL.initExtensions(context);
      }
      return handle;
    }, makeContextCurrent: function(contextHandle) {
      GL.currentContext = GL.contexts[contextHandle];
      Module2.ctx = GLctx = GL.currentContext && GL.currentContext.GLctx;
      return !(contextHandle && !GLctx);
    }, getContext: function(contextHandle) {
      return GL.contexts[contextHandle];
    }, deleteContext: function(contextHandle) {
      if (GL.currentContext === GL.contexts[contextHandle])
        GL.currentContext = null;
      if (typeof JSEvents == "object")
        JSEvents.removeAllHandlersOnTarget(GL.contexts[contextHandle].GLctx.canvas);
      if (GL.contexts[contextHandle] && GL.contexts[contextHandle].GLctx.canvas)
        GL.contexts[contextHandle].GLctx.canvas.GLctxObject = undefined;
      GL.contexts[contextHandle] = null;
    }, initExtensions: function(context) {
      if (!context)
        context = GL.currentContext;
      if (context.initExtensionsDone)
        return;
      context.initExtensionsDone = true;
      var GLctx2 = context.GLctx;
      __webgl_enable_ANGLE_instanced_arrays(GLctx2);
      __webgl_enable_OES_vertex_array_object(GLctx2);
      __webgl_enable_WEBGL_draw_buffers(GLctx2);
      {
        GLctx2.disjointTimerQueryExt = GLctx2.getExtension("EXT_disjoint_timer_query");
      }
      __webgl_enable_WEBGL_multi_draw(GLctx2);
      var exts = GLctx2.getSupportedExtensions() || [];
      exts.forEach(function(ext) {
        if (!ext.includes("lose_context") && !ext.includes("debug")) {
          GLctx2.getExtension(ext);
        }
      });
    } };
    function webglGetLeftBracePos(name) {
      return name.slice(-1) == "]" && name.lastIndexOf("[");
    }
    function webglPrepareUniformLocationsBeforeFirstUse(program) {
      var { uniformLocsById, uniformSizeAndIdsByName } = program, i2, j;
      if (!uniformLocsById) {
        program.uniformLocsById = uniformLocsById = {};
        program.uniformArrayNamesById = {};
        for (i2 = 0;i2 < GLctx.getProgramParameter(program, 35718); ++i2) {
          var u = GLctx.getActiveUniform(program, i2);
          var nm = u.name;
          var sz = u.size;
          var lb = webglGetLeftBracePos(nm);
          var arrayName = lb > 0 ? nm.slice(0, lb) : nm;
          var id = program.uniformIdCounter;
          program.uniformIdCounter += sz;
          uniformSizeAndIdsByName[arrayName] = [sz, id];
          for (j = 0;j < sz; ++j) {
            uniformLocsById[id] = j;
            program.uniformArrayNamesById[id++] = arrayName;
          }
        }
      }
    }
    function _glGetUniformLocation(program, name) {
      name = UTF8ToString(name);
      if (program = GL.programs[program]) {
        webglPrepareUniformLocationsBeforeFirstUse(program);
        var uniformLocsById = program.uniformLocsById;
        var arrayIndex = 0;
        var uniformBaseName = name;
        var leftBrace = webglGetLeftBracePos(name);
        if (leftBrace > 0) {
          arrayIndex = jstoi_q(name.slice(leftBrace + 1)) >>> 0;
          uniformBaseName = name.slice(0, leftBrace);
        }
        var sizeAndId = program.uniformSizeAndIdsByName[uniformBaseName];
        if (sizeAndId && arrayIndex < sizeAndId[0]) {
          arrayIndex += sizeAndId[1];
          if (uniformLocsById[arrayIndex] = uniformLocsById[arrayIndex] || GLctx.getUniformLocation(program, name)) {
            return arrayIndex;
          }
        }
      } else {
        GL.recordError(1281);
      }
      return -1;
    }
    function webglGetUniformLocation(location) {
      var p = GLctx.currentProgram;
      if (p) {
        var webglLoc = p.uniformLocsById[location];
        if (typeof webglLoc == "number") {
          p.uniformLocsById[location] = webglLoc = GLctx.getUniformLocation(p, p.uniformArrayNamesById[location] + (webglLoc > 0 ? "[" + webglLoc + "]" : ""));
        }
        return webglLoc;
      } else {
        GL.recordError(1282);
      }
    }
    function _glUniform1f(location, v0) {
      GLctx.uniform1f(webglGetUniformLocation(location), v0);
    }
    function _glUniform1i(location, v0) {
      GLctx.uniform1i(webglGetUniformLocation(location), v0);
    }
    var miniTempWebGLFloatBuffers = [];
    function _glUniform2fv(location, count, value) {
      if (count <= 144) {
        var view = miniTempWebGLFloatBuffers[2 * count - 1];
        for (var i2 = 0;i2 < 2 * count; i2 += 2) {
          view[i2] = HEAPF32[value + 4 * i2 >> 2];
          view[i2 + 1] = HEAPF32[value + (4 * i2 + 4) >> 2];
        }
      } else {
        var view = HEAPF32.subarray(value >> 2, value + count * 8 >> 2);
      }
      GLctx.uniform2fv(webglGetUniformLocation(location), view);
    }
    var __miniTempWebGLIntBuffers = [];
    function _glUniform2iv(location, count, value) {
      if (count <= 144) {
        var view = __miniTempWebGLIntBuffers[2 * count - 1];
        for (var i2 = 0;i2 < 2 * count; i2 += 2) {
          view[i2] = HEAP32[value + 4 * i2 >> 2];
          view[i2 + 1] = HEAP32[value + (4 * i2 + 4) >> 2];
        }
      } else {
        var view = HEAP32.subarray(value >> 2, value + count * 8 >> 2);
      }
      GLctx.uniform2iv(webglGetUniformLocation(location), view);
    }
    function _glUniform3fv(location, count, value) {
      if (count <= 96) {
        var view = miniTempWebGLFloatBuffers[3 * count - 1];
        for (var i2 = 0;i2 < 3 * count; i2 += 3) {
          view[i2] = HEAPF32[value + 4 * i2 >> 2];
          view[i2 + 1] = HEAPF32[value + (4 * i2 + 4) >> 2];
          view[i2 + 2] = HEAPF32[value + (4 * i2 + 8) >> 2];
        }
      } else {
        var view = HEAPF32.subarray(value >> 2, value + count * 12 >> 2);
      }
      GLctx.uniform3fv(webglGetUniformLocation(location), view);
    }
    function _glUniform3iv(location, count, value) {
      if (count <= 96) {
        var view = __miniTempWebGLIntBuffers[3 * count - 1];
        for (var i2 = 0;i2 < 3 * count; i2 += 3) {
          view[i2] = HEAP32[value + 4 * i2 >> 2];
          view[i2 + 1] = HEAP32[value + (4 * i2 + 4) >> 2];
          view[i2 + 2] = HEAP32[value + (4 * i2 + 8) >> 2];
        }
      } else {
        var view = HEAP32.subarray(value >> 2, value + count * 12 >> 2);
      }
      GLctx.uniform3iv(webglGetUniformLocation(location), view);
    }
    function _glUniform4fv(location, count, value) {
      if (count <= 72) {
        var view = miniTempWebGLFloatBuffers[4 * count - 1];
        var heap = HEAPF32;
        value >>= 2;
        for (var i2 = 0;i2 < 4 * count; i2 += 4) {
          var dst = value + i2;
          view[i2] = heap[dst];
          view[i2 + 1] = heap[dst + 1];
          view[i2 + 2] = heap[dst + 2];
          view[i2 + 3] = heap[dst + 3];
        }
      } else {
        var view = HEAPF32.subarray(value >> 2, value + count * 16 >> 2);
      }
      GLctx.uniform4fv(webglGetUniformLocation(location), view);
    }
    function _glUniform4iv(location, count, value) {
      if (count <= 72) {
        var view = __miniTempWebGLIntBuffers[4 * count - 1];
        for (var i2 = 0;i2 < 4 * count; i2 += 4) {
          view[i2] = HEAP32[value + 4 * i2 >> 2];
          view[i2 + 1] = HEAP32[value + (4 * i2 + 4) >> 2];
          view[i2 + 2] = HEAP32[value + (4 * i2 + 8) >> 2];
          view[i2 + 3] = HEAP32[value + (4 * i2 + 12) >> 2];
        }
      } else {
        var view = HEAP32.subarray(value >> 2, value + count * 16 >> 2);
      }
      GLctx.uniform4iv(webglGetUniformLocation(location), view);
    }
    function _setTempRet0(val) {
      setTempRet0(val);
    }
    function __isLeapYear(year) {
      return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    }
    function __arraySum(array, index) {
      var sum = 0;
      for (var i2 = 0;i2 <= index; sum += array[i2++]) {}
      return sum;
    }
    var __MONTH_DAYS_LEAP = [31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
    var __MONTH_DAYS_REGULAR = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
    function __addDays(date, days) {
      var newDate = new Date(date.getTime());
      while (days > 0) {
        var leap = __isLeapYear(newDate.getFullYear());
        var currentMonth = newDate.getMonth();
        var daysInCurrentMonth = (leap ? __MONTH_DAYS_LEAP : __MONTH_DAYS_REGULAR)[currentMonth];
        if (days > daysInCurrentMonth - newDate.getDate()) {
          days -= daysInCurrentMonth - newDate.getDate() + 1;
          newDate.setDate(1);
          if (currentMonth < 11) {
            newDate.setMonth(currentMonth + 1);
          } else {
            newDate.setMonth(0);
            newDate.setFullYear(newDate.getFullYear() + 1);
          }
        } else {
          newDate.setDate(newDate.getDate() + days);
          return newDate;
        }
      }
      return newDate;
    }
    function _strftime(s, maxsize, format, tm) {
      var tm_zone = HEAP32[tm + 40 >> 2];
      var date = { tm_sec: HEAP32[tm >> 2], tm_min: HEAP32[tm + 4 >> 2], tm_hour: HEAP32[tm + 8 >> 2], tm_mday: HEAP32[tm + 12 >> 2], tm_mon: HEAP32[tm + 16 >> 2], tm_year: HEAP32[tm + 20 >> 2], tm_wday: HEAP32[tm + 24 >> 2], tm_yday: HEAP32[tm + 28 >> 2], tm_isdst: HEAP32[tm + 32 >> 2], tm_gmtoff: HEAP32[tm + 36 >> 2], tm_zone: tm_zone ? UTF8ToString(tm_zone) : "" };
      var pattern = UTF8ToString(format);
      var EXPANSION_RULES_1 = { "%c": "%a %b %d %H:%M:%S %Y", "%D": "%m/%d/%y", "%F": "%Y-%m-%d", "%h": "%b", "%r": "%I:%M:%S %p", "%R": "%H:%M", "%T": "%H:%M:%S", "%x": "%m/%d/%y", "%X": "%H:%M:%S", "%Ec": "%c", "%EC": "%C", "%Ex": "%m/%d/%y", "%EX": "%H:%M:%S", "%Ey": "%y", "%EY": "%Y", "%Od": "%d", "%Oe": "%e", "%OH": "%H", "%OI": "%I", "%Om": "%m", "%OM": "%M", "%OS": "%S", "%Ou": "%u", "%OU": "%U", "%OV": "%V", "%Ow": "%w", "%OW": "%W", "%Oy": "%y" };
      for (var rule in EXPANSION_RULES_1) {
        pattern = pattern.replace(new RegExp(rule, "g"), EXPANSION_RULES_1[rule]);
      }
      var WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
      var MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
      function leadingSomething(value, digits, character) {
        var str = typeof value == "number" ? value.toString() : value || "";
        while (str.length < digits) {
          str = character[0] + str;
        }
        return str;
      }
      function leadingNulls(value, digits) {
        return leadingSomething(value, digits, "0");
      }
      function compareByDay(date1, date2) {
        function sgn(value) {
          return value < 0 ? -1 : value > 0 ? 1 : 0;
        }
        var compare;
        if ((compare = sgn(date1.getFullYear() - date2.getFullYear())) === 0) {
          if ((compare = sgn(date1.getMonth() - date2.getMonth())) === 0) {
            compare = sgn(date1.getDate() - date2.getDate());
          }
        }
        return compare;
      }
      function getFirstWeekStartDate(janFourth) {
        switch (janFourth.getDay()) {
          case 0:
            return new Date(janFourth.getFullYear() - 1, 11, 29);
          case 1:
            return janFourth;
          case 2:
            return new Date(janFourth.getFullYear(), 0, 3);
          case 3:
            return new Date(janFourth.getFullYear(), 0, 2);
          case 4:
            return new Date(janFourth.getFullYear(), 0, 1);
          case 5:
            return new Date(janFourth.getFullYear() - 1, 11, 31);
          case 6:
            return new Date(janFourth.getFullYear() - 1, 11, 30);
        }
      }
      function getWeekBasedYear(date2) {
        var thisDate = __addDays(new Date(date2.tm_year + 1900, 0, 1), date2.tm_yday);
        var janFourthThisYear = new Date(thisDate.getFullYear(), 0, 4);
        var janFourthNextYear = new Date(thisDate.getFullYear() + 1, 0, 4);
        var firstWeekStartThisYear = getFirstWeekStartDate(janFourthThisYear);
        var firstWeekStartNextYear = getFirstWeekStartDate(janFourthNextYear);
        if (compareByDay(firstWeekStartThisYear, thisDate) <= 0) {
          if (compareByDay(firstWeekStartNextYear, thisDate) <= 0) {
            return thisDate.getFullYear() + 1;
          } else {
            return thisDate.getFullYear();
          }
        } else {
          return thisDate.getFullYear() - 1;
        }
      }
      var EXPANSION_RULES_2 = { "%a": function(date2) {
        return WEEKDAYS[date2.tm_wday].substring(0, 3);
      }, "%A": function(date2) {
        return WEEKDAYS[date2.tm_wday];
      }, "%b": function(date2) {
        return MONTHS[date2.tm_mon].substring(0, 3);
      }, "%B": function(date2) {
        return MONTHS[date2.tm_mon];
      }, "%C": function(date2) {
        var year = date2.tm_year + 1900;
        return leadingNulls(year / 100 | 0, 2);
      }, "%d": function(date2) {
        return leadingNulls(date2.tm_mday, 2);
      }, "%e": function(date2) {
        return leadingSomething(date2.tm_mday, 2, " ");
      }, "%g": function(date2) {
        return getWeekBasedYear(date2).toString().substring(2);
      }, "%G": function(date2) {
        return getWeekBasedYear(date2);
      }, "%H": function(date2) {
        return leadingNulls(date2.tm_hour, 2);
      }, "%I": function(date2) {
        var twelveHour = date2.tm_hour;
        if (twelveHour == 0)
          twelveHour = 12;
        else if (twelveHour > 12)
          twelveHour -= 12;
        return leadingNulls(twelveHour, 2);
      }, "%j": function(date2) {
        return leadingNulls(date2.tm_mday + __arraySum(__isLeapYear(date2.tm_year + 1900) ? __MONTH_DAYS_LEAP : __MONTH_DAYS_REGULAR, date2.tm_mon - 1), 3);
      }, "%m": function(date2) {
        return leadingNulls(date2.tm_mon + 1, 2);
      }, "%M": function(date2) {
        return leadingNulls(date2.tm_min, 2);
      }, "%n": function() {
        return `
`;
      }, "%p": function(date2) {
        if (date2.tm_hour >= 0 && date2.tm_hour < 12) {
          return "AM";
        } else {
          return "PM";
        }
      }, "%S": function(date2) {
        return leadingNulls(date2.tm_sec, 2);
      }, "%t": function() {
        return "\t";
      }, "%u": function(date2) {
        return date2.tm_wday || 7;
      }, "%U": function(date2) {
        var days = date2.tm_yday + 7 - date2.tm_wday;
        return leadingNulls(Math.floor(days / 7), 2);
      }, "%V": function(date2) {
        var val = Math.floor((date2.tm_yday + 7 - (date2.tm_wday + 6) % 7) / 7);
        if ((date2.tm_wday + 371 - date2.tm_yday - 2) % 7 <= 2) {
          val++;
        }
        if (!val) {
          val = 52;
          var dec31 = (date2.tm_wday + 7 - date2.tm_yday - 1) % 7;
          if (dec31 == 4 || dec31 == 5 && __isLeapYear(date2.tm_year % 400 - 1)) {
            val++;
          }
        } else if (val == 53) {
          var jan1 = (date2.tm_wday + 371 - date2.tm_yday) % 7;
          if (jan1 != 4 && (jan1 != 3 || !__isLeapYear(date2.tm_year)))
            val = 1;
        }
        return leadingNulls(val, 2);
      }, "%w": function(date2) {
        return date2.tm_wday;
      }, "%W": function(date2) {
        var days = date2.tm_yday + 7 - (date2.tm_wday + 6) % 7;
        return leadingNulls(Math.floor(days / 7), 2);
      }, "%y": function(date2) {
        return (date2.tm_year + 1900).toString().substring(2);
      }, "%Y": function(date2) {
        return date2.tm_year + 1900;
      }, "%z": function(date2) {
        var off = date2.tm_gmtoff;
        var ahead = off >= 0;
        off = Math.abs(off) / 60;
        off = off / 60 * 100 + off % 60;
        return (ahead ? "+" : "-") + String("0000" + off).slice(-4);
      }, "%Z": function(date2) {
        return date2.tm_zone;
      }, "%%": function() {
        return "%";
      } };
      pattern = pattern.replace(/%%/g, "\x00\x00");
      for (var rule in EXPANSION_RULES_2) {
        if (pattern.includes(rule)) {
          pattern = pattern.replace(new RegExp(rule, "g"), EXPANSION_RULES_2[rule](date));
        }
      }
      pattern = pattern.replace(/\0\0/g, "%");
      var bytes = intArrayFromString(pattern, false);
      if (bytes.length > maxsize) {
        return 0;
      }
      writeArrayToMemory(bytes, s);
      return bytes.length - 1;
    }
    function _strftime_l(s, maxsize, format, tm) {
      return _strftime(s, maxsize, format, tm);
    }
    var FSNode = function(parent, name, mode, rdev) {
      if (!parent) {
        parent = this;
      }
      this.parent = parent;
      this.mount = parent.mount;
      this.mounted = null;
      this.id = FS.nextInode++;
      this.name = name;
      this.mode = mode;
      this.node_ops = {};
      this.stream_ops = {};
      this.rdev = rdev;
    };
    var readMode = 292 | 73;
    var writeMode = 146;
    Object.defineProperties(FSNode.prototype, { read: { get: function() {
      return (this.mode & readMode) === readMode;
    }, set: function(val) {
      val ? this.mode |= readMode : this.mode &= ~readMode;
    } }, write: { get: function() {
      return (this.mode & writeMode) === writeMode;
    }, set: function(val) {
      val ? this.mode |= writeMode : this.mode &= ~writeMode;
    } }, isFolder: { get: function() {
      return FS.isDir(this.mode);
    } }, isDevice: { get: function() {
      return FS.isChrdev(this.mode);
    } } });
    FS.FSNode = FSNode;
    FS.staticInit();
    embind_init_charCodes();
    BindingError = Module2["BindingError"] = extendError(Error, "BindingError");
    InternalError = Module2["InternalError"] = extendError(Error, "InternalError");
    init_ClassHandle();
    init_embind();
    init_RegisteredPointer();
    UnboundTypeError = Module2["UnboundTypeError"] = extendError(Error, "UnboundTypeError");
    init_emval();
    var GLctx;
    var miniTempWebGLFloatBuffersStorage = new Float32Array(288);
    for (var i = 0;i < 288; ++i) {
      miniTempWebGLFloatBuffers[i] = miniTempWebGLFloatBuffersStorage.subarray(0, i + 1);
    }
    var __miniTempWebGLIntBuffersStorage = new Int32Array(288);
    for (var i = 0;i < 288; ++i) {
      __miniTempWebGLIntBuffers[i] = __miniTempWebGLIntBuffersStorage.subarray(0, i + 1);
    }
    function intArrayFromString(stringy, dontAddNull, length) {
      var len = length > 0 ? length : lengthBytesUTF8(stringy) + 1;
      var u8array = new Array(len);
      var numBytesWritten = stringToUTF8Array(stringy, u8array, 0, u8array.length);
      if (dontAddNull)
        u8array.length = numBytesWritten;
      return u8array;
    }
    var asmLibraryArg = { ga: OSD_MemInfo_getModuleHeapLength, k: ___assert_fail, b: ___cxa_allocate_exception, c: ___cxa_throw, Q: ___syscall_chmod, R: ___syscall_faccessat, q: ___syscall_fcntl64, K: ___syscall_fstat64, U: ___syscall_ioctl, I: ___syscall_lstat64, H: ___syscall_newfstatat, u: ___syscall_openat, J: ___syscall_stat64, z: __embind_register_bigint, W: __embind_register_bool, d: __embind_register_class, f: __embind_register_class_class_function, e: __embind_register_class_constructor, a: __embind_register_class_function, V: __embind_register_emval, g: __embind_register_enum, h: __embind_register_enum_value, v: __embind_register_float, j: __embind_register_integer, i: __embind_register_memory_view, w: __embind_register_std_string, r: __embind_register_std_wstring, X: __embind_register_void, N: __emscripten_date_now, M: __emscripten_get_now_is_monotonic, m: __emval_as, ka: __emval_call_method, x: __emval_decref, la: __emval_get_global, ja: __emval_get_method_caller, s: __emval_get_property, E: __emval_incref, ia: __emval_new_cstring, ma: __emval_run_destructors, n: __emval_set_property, l: __emval_take_value, na: __emval_typeof, O: __localtime_js, D: __munmap_js, P: __tzset_js, t: _abort, C: _emscripten_get_heap_max, L: _emscripten_memcpy_big, o: _emscripten_resize_heap, F: _environ_get, G: _environ_sizes_get, fa: _exit, p: _fd_close, T: _fd_read, y: _fd_seek, S: _fd_write, ha: _gethostbyname, ea: _glGetUniformLocation, ca: _glUniform1f, da: _glUniform1i, _: _glUniform2fv, ba: _glUniform2iv, Z: _glUniform3fv, aa: _glUniform3iv, Y: _glUniform4fv, $: _glUniform4iv, A: _setTempRet0, B: _strftime_l };
    var asm = createWasm();
    var ___wasm_call_ctors = Module2["___wasm_call_ctors"] = function() {
      return (___wasm_call_ctors = Module2["___wasm_call_ctors"] = Module2["asm"]["pa"]).apply(null, arguments);
    };
    var _malloc = Module2["_malloc"] = function() {
      return (_malloc = Module2["_malloc"] = Module2["asm"]["qa"]).apply(null, arguments);
    };
    var _free = Module2["_free"] = function() {
      return (_free = Module2["_free"] = Module2["asm"]["ra"]).apply(null, arguments);
    };
    var ___errno_location = Module2["___errno_location"] = function() {
      return (___errno_location = Module2["___errno_location"] = Module2["asm"]["sa"]).apply(null, arguments);
    };
    var ___getTypeName = Module2["___getTypeName"] = function() {
      return (___getTypeName = Module2["___getTypeName"] = Module2["asm"]["ta"]).apply(null, arguments);
    };
    var ___embind_register_native_and_builtin_types = Module2["___embind_register_native_and_builtin_types"] = function() {
      return (___embind_register_native_and_builtin_types = Module2["___embind_register_native_and_builtin_types"] = Module2["asm"]["ua"]).apply(null, arguments);
    };
    var _htons = Module2["_htons"] = function() {
      return (_htons = Module2["_htons"] = Module2["asm"]["wa"]).apply(null, arguments);
    };
    var _emscripten_builtin_memalign = Module2["_emscripten_builtin_memalign"] = function() {
      return (_emscripten_builtin_memalign = Module2["_emscripten_builtin_memalign"] = Module2["asm"]["xa"]).apply(null, arguments);
    };
    var ___cxa_is_pointer_type = Module2["___cxa_is_pointer_type"] = function() {
      return (___cxa_is_pointer_type = Module2["___cxa_is_pointer_type"] = Module2["asm"]["ya"]).apply(null, arguments);
    };
    var dynCall_jiji = Module2["dynCall_jiji"] = function() {
      return (dynCall_jiji = Module2["dynCall_jiji"] = Module2["asm"]["za"]).apply(null, arguments);
    };
    var dynCall_viiiiji = Module2["dynCall_viiiiji"] = function() {
      return (dynCall_viiiiji = Module2["dynCall_viiiiji"] = Module2["asm"]["Aa"]).apply(null, arguments);
    };
    var dynCall_viijii = Module2["dynCall_viijii"] = function() {
      return (dynCall_viijii = Module2["dynCall_viijii"] = Module2["asm"]["Ba"]).apply(null, arguments);
    };
    var dynCall_iiiiij = Module2["dynCall_iiiiij"] = function() {
      return (dynCall_iiiiij = Module2["dynCall_iiiiij"] = Module2["asm"]["Ca"]).apply(null, arguments);
    };
    var dynCall_iiiiijj = Module2["dynCall_iiiiijj"] = function() {
      return (dynCall_iiiiijj = Module2["dynCall_iiiiijj"] = Module2["asm"]["Da"]).apply(null, arguments);
    };
    var dynCall_iiiiiijj = Module2["dynCall_iiiiiijj"] = function() {
      return (dynCall_iiiiiijj = Module2["dynCall_iiiiiijj"] = Module2["asm"]["Ea"]).apply(null, arguments);
    };
    Module2["FS"] = FS;
    var calledRun;
    function ExitStatus(status) {
      this.name = "ExitStatus";
      this.message = "Program terminated with exit(" + status + ")";
      this.status = status;
    }
    dependenciesFulfilled = function runCaller() {
      if (!calledRun)
        run();
      if (!calledRun)
        dependenciesFulfilled = runCaller;
    };
    function run(args) {
      args = args || arguments_;
      if (runDependencies > 0) {
        return;
      }
      preRun();
      if (runDependencies > 0) {
        return;
      }
      function doRun() {
        if (calledRun)
          return;
        calledRun = true;
        Module2["calledRun"] = true;
        if (ABORT)
          return;
        initRuntime();
        readyPromiseResolve(Module2);
        if (Module2["onRuntimeInitialized"])
          Module2["onRuntimeInitialized"]();
        postRun();
      }
      if (Module2["setStatus"]) {
        Module2["setStatus"]("Running...");
        setTimeout(function() {
          setTimeout(function() {
            Module2["setStatus"]("");
          }, 1);
          doRun();
        }, 1);
      } else {
        doRun();
      }
    }
    Module2["run"] = run;
    function exit(status, implicit) {
      EXITSTATUS = status;
      procExit(status);
    }
    function procExit(code) {
      EXITSTATUS = code;
      if (!keepRuntimeAlive()) {
        if (Module2["onExit"])
          Module2["onExit"](code);
        ABORT = true;
      }
      quit_(code, new ExitStatus(code));
    }
    if (Module2["preInit"]) {
      if (typeof Module2["preInit"] == "function")
        Module2["preInit"] = [Module2["preInit"]];
      while (Module2["preInit"].length > 0) {
        Module2["preInit"].pop()();
      }
    }
    run();
    return Module2.ready;
  };
})();
var replicad_single_default = Module;

// node_modules/replicad/dist/replicad.js
var OC = {
  library: null
};
var setOC = (oc) => {
  OC.library = oc;
};
var getOC = () => {
  if (!OC.library)
    throw new Error("oppencascade has not been loaded");
  return OC.library;
};
if (!globalThis.FinalizationRegistry) {
  console.log("Garbage collection will not work");
  globalThis.FinalizationRegistry = () => ({
    register: () => null,
    unregister: () => null
  });
}
var deletetableRegistry = new globalThis.FinalizationRegistry((heldValue) => {
  try {
    heldValue.delete();
  } catch (e) {
    console.error(e);
  }
});

class WrappingObj {
  constructor(wrapped) {
    this.oc = getOC();
    if (wrapped) {
      deletetableRegistry.register(this, wrapped, wrapped);
    }
    this._wrapped = wrapped;
  }
  get wrapped() {
    if (this._wrapped === null)
      throw new Error("This object has been deleted");
    return this._wrapped;
  }
  set wrapped(newWrapped) {
    if (this._wrapped) {
      deletetableRegistry.unregister(this._wrapped);
      this._wrapped.delete();
    }
    deletetableRegistry.register(this, newWrapped, newWrapped);
    this._wrapped = newWrapped;
  }
  delete() {
    var _a;
    deletetableRegistry.unregister(this.wrapped);
    (_a = this.wrapped) == null || _a.delete();
    this._wrapped = null;
  }
}
var GCWithScope = () => {
  function gcWithScope(value) {
    deletetableRegistry.register(gcWithScope, value);
    return value;
  }
  return gcWithScope;
};
var localGC = (debug) => {
  const cleaner = /* @__PURE__ */ new Set;
  return [
    (v) => {
      cleaner.add(v);
      return v;
    },
    () => {
      [...cleaner.values()].forEach((d) => d.delete());
      cleaner.clear();
    },
    debug ? cleaner : undefined
  ];
};
var HASH_CODE_MAX = 2147483647;
var DEG2RAD = Math.PI / 180;
var RAD2DEG = 180 / Math.PI;
var round3 = (v) => Math.round(v * 1000) / 1000;
var makeAx3 = (center, dir, xDir) => {
  const oc = getOC();
  const origin = asPnt(center);
  const direction = asDir(dir);
  let axis;
  if (xDir) {
    const xDirection = asDir(xDir);
    axis = new oc.gp_Ax3_3(origin, direction, xDirection);
    xDirection.delete();
  } else {
    axis = new oc.gp_Ax3_4(origin, direction);
  }
  origin.delete();
  direction.delete();
  return axis;
};
function makePln(origin, dir) {
  const orig = asPnt(origin);
  const direction = asDir(dir);
  const oc = getOC();
  const pln = new oc.gp_Pln_3(orig, direction);
  orig.delete();
  direction.delete();
  return pln;
}
var makeAx2 = (center, dir, xDir) => {
  const oc = getOC();
  const origin = asPnt(center);
  const direction = asDir(dir);
  let axis;
  if (xDir) {
    const xDirection = asDir(xDir);
    axis = new oc.gp_Ax2_2(origin, direction, xDirection);
    xDirection.delete();
  } else {
    axis = new oc.gp_Ax2_3(origin, direction);
  }
  origin.delete();
  direction.delete();
  return axis;
};
var makeAx1 = (center, dir) => {
  const oc = getOC();
  const origin = asPnt(center);
  const direction = asDir(dir);
  const axis = new oc.gp_Ax1_2(origin, direction);
  origin.delete();
  direction.delete();
  return axis;
};
var makeVec = (vector = [0, 0, 0]) => {
  const oc = getOC();
  if (Array.isArray(vector)) {
    if (vector.length === 3)
      return new oc.gp_Vec_4(...vector);
    else if (vector.length === 2)
      return new oc.gp_Vec_4(...vector, 0);
  } else if (vector instanceof Vector) {
    return new oc.gp_Vec_3(vector.wrapped.XYZ());
  } else if (vector.XYZ)
    return new oc.gp_Vec_3(vector.XYZ());
  return new oc.gp_Vec_4(0, 0, 0);
};

class Vector extends WrappingObj {
  constructor(vector = [0, 0, 0]) {
    super(makeVec(vector));
  }
  get repr() {
    return `x: ${round3(this.x)}, y: ${round3(this.y)}, z: ${round3(this.z)}`;
  }
  get x() {
    return this.wrapped.X();
  }
  get y() {
    return this.wrapped.Y();
  }
  get z() {
    return this.wrapped.Z();
  }
  get Length() {
    return this.wrapped.Magnitude();
  }
  toTuple() {
    return [this.x, this.y, this.z];
  }
  cross(v) {
    return new Vector(this.wrapped.Crossed(v.wrapped));
  }
  dot(v) {
    return this.wrapped.Dot(v.wrapped);
  }
  sub(v) {
    return new Vector(this.wrapped.Subtracted(v.wrapped));
  }
  add(v) {
    return new Vector(this.wrapped.Added(v.wrapped));
  }
  multiply(scale2) {
    return new Vector(this.wrapped.Multiplied(scale2));
  }
  normalized() {
    return new Vector(this.wrapped.Normalized());
  }
  normalize() {
    this.wrapped.Normalize();
    return this;
  }
  getCenter() {
    return this;
  }
  getAngle(v) {
    return this.wrapped.Angle(v.wrapped) * RAD2DEG;
  }
  projectToPlane(plane) {
    const base = plane.origin;
    const normal = plane.zDir;
    const v1 = this.sub(base);
    const v2 = normal.multiply(v1.dot(normal) / normal.Length ** 2);
    const projection = this.sub(v2);
    v1.delete();
    v2.delete();
    return projection;
  }
  equals(other) {
    return this.wrapped.IsEqual(other.wrapped, 0.00001, 0.00001);
  }
  toPnt() {
    return new this.oc.gp_Pnt_2(this.wrapped.XYZ());
  }
  toDir() {
    return new this.oc.gp_Dir_3(this.wrapped.XYZ());
  }
  rotate(angle, center = [0, 0, 0], direction = [0, 0, 1]) {
    const ax = makeAx1(center, direction);
    this.wrapped.Rotate(ax, angle * DEG2RAD);
    ax.delete();
    return this;
  }
}
var DIRECTIONS$1 = {
  X: [1, 0, 0],
  Y: [0, 1, 0],
  Z: [0, 0, 1]
};
function makeDirection(p) {
  if (p === "X" || p === "Y" || p === "Z") {
    return DIRECTIONS$1[p];
  }
  return p;
}
function asPnt(coords) {
  const v = new Vector(coords);
  const pnt2 = v.toPnt();
  v.delete();
  return pnt2;
}
function asDir(coords) {
  const v = new Vector(coords);
  const dir = v.toDir();
  v.delete();
  return dir;
}

class Transformation extends WrappingObj {
  constructor(transform) {
    const oc = getOC();
    super(transform || new oc.gp_Trsf_1);
  }
  clone() {
    const clone = this.wrapped.Inverted();
    clone.Invert();
    return new Transformation(clone);
  }
  translate(xDistOrVector, yDist = 0, zDist = 0) {
    const translation = new Vector(typeof xDistOrVector === "number" ? [xDistOrVector, yDist, zDist] : xDistOrVector);
    this.wrapped.SetTranslation_1(translation.wrapped);
    return this;
  }
  rotate(angle, position = [0, 0, 0], direction = [0, 0, 1]) {
    const dir = asDir(direction);
    const origin = asPnt(position);
    const axis = new this.oc.gp_Ax1_2(origin, dir);
    this.wrapped.SetRotation_1(axis, angle * DEG2RAD);
    axis.delete();
    dir.delete();
    origin.delete();
    return this;
  }
  mirror(inputPlane = "YZ", inputOrigin) {
    const r = GCWithScope();
    let origin;
    let direction;
    if (typeof inputPlane === "string") {
      const plane = r(createNamedPlane(inputPlane, inputOrigin));
      origin = plane.origin;
      direction = plane.zDir;
    } else if (inputPlane instanceof Plane) {
      origin = inputOrigin || inputPlane.origin;
      direction = inputPlane.zDir;
    } else {
      origin = inputOrigin || [0, 0, 0];
      direction = inputPlane;
    }
    const mirrorAxis = r(makeAx2(origin, direction));
    this.wrapped.SetMirror_3(mirrorAxis);
    return this;
  }
  scale(center, scale2) {
    const pnt2 = asPnt(center);
    this.wrapped.SetScale(pnt2, scale2);
    pnt2.delete();
    return this;
  }
  inverse() {
    this.wrapped.Invert();
    return this;
  }
  inverted() {
    return new Transformation(this.wrapped.Inverted());
  }
  coordSystemChange(fromSystem, toSystem) {
    const r = GCWithScope();
    const fromAx = r(fromSystem === "reference" ? new this.oc.gp_Ax3_1 : makeAx3(fromSystem.origin, fromSystem.zDir, fromSystem.xDir));
    const toAx = r(toSystem === "reference" ? new this.oc.gp_Ax3_1 : makeAx3(toSystem.origin, toSystem.zDir, toSystem.xDir));
    this.wrapped.SetTransformation_1(fromAx, toAx);
    return this;
  }
  transformPoint(point) {
    const pnt2 = asPnt(point);
    const newPoint = pnt2.Transformed(this.wrapped);
    pnt2.delete();
    return newPoint;
  }
  transform(shape) {
    const transformer = new this.oc.BRepBuilderAPI_Transform_2(shape, this.wrapped, true);
    return transformer.ModifiedShape(shape);
  }
}

class Plane {
  constructor(origin, xDirection = null, normal = [0, 0, 1]) {
    this.oc = getOC();
    const zDir = new Vector(normal);
    if (zDir.Length === 0) {
      throw new Error("normal should be non null");
    }
    this.zDir = zDir.normalize();
    let xDir;
    if (!xDirection) {
      const ax3 = makeAx3(origin, zDir);
      xDir = new Vector(ax3.XDirection());
      ax3.delete();
    } else {
      xDir = new Vector(xDirection);
    }
    if (xDir.Length === 0) {
      throw new Error("xDir should be non null");
    }
    this.xDir = xDir.normalize();
    this.yDir = this.zDir.cross(this.xDir).normalize();
    this.origin = new Vector(origin);
  }
  delete() {
    this.localToGlobal.delete();
    this.xDir.delete();
    this.yDir.delete();
    this.zDir.delete();
    this._origin.delete();
  }
  clone() {
    return new Plane(this.origin, this.xDir, this.zDir);
  }
  get origin() {
    return this._origin;
  }
  set origin(newOrigin) {
    this._origin = newOrigin;
    this._calcTransforms();
  }
  translateTo(point) {
    const newPlane = this.clone();
    newPlane.origin = new Vector(point);
    return newPlane;
  }
  translate(xDistOrVector, yDist = 0, zDist = 0) {
    const translation = new Vector(typeof xDistOrVector === "number" ? [xDistOrVector, yDist, zDist] : xDistOrVector);
    return this.translateTo(this.origin.add(translation));
  }
  translateX(xDist) {
    return this.translate(xDist, 0, 0);
  }
  translateY(yDist) {
    return this.translate(0, yDist, 0);
  }
  translateZ(zDist) {
    return this.translate(0, 0, zDist);
  }
  pivot(angle, direction = [1, 0, 0]) {
    const dir = makeDirection(direction);
    const zDir = new Vector(this.zDir).rotate(angle, [0, 0, 0], dir);
    const xDir = new Vector(this.xDir).rotate(angle, [0, 0, 0], dir);
    return new Plane(this.origin, xDir, zDir);
  }
  rotate2DAxes(angle) {
    const xDir = new Vector(this.xDir).rotate(angle, [0, 0, 0], this.zDir);
    return new Plane(this.origin, xDir, this.zDir);
  }
  _calcTransforms() {
    const globalCoordSystem = new this.oc.gp_Ax3_1;
    const localCoordSystem = makeAx3(this.origin, this.zDir, this.xDir);
    const forwardT = new this.oc.gp_Trsf_1;
    forwardT.SetTransformation_1(globalCoordSystem, localCoordSystem);
    this.globalToLocal = new Transformation;
    this.globalToLocal.coordSystemChange("reference", {
      origin: this.origin,
      zDir: this.zDir,
      xDir: this.xDir
    });
    this.localToGlobal = new Transformation;
    this.localToGlobal.coordSystemChange({
      origin: this.origin,
      zDir: this.zDir,
      xDir: this.xDir
    }, "reference");
  }
  setOrigin2d(x, y) {
    this.origin = this.toWorldCoords([x, y]);
  }
  toLocalCoords(vec2) {
    const pnt2 = this.globalToLocal.transformPoint(vec2);
    const newVec = new Vector(pnt2);
    pnt2.delete();
    return newVec;
  }
  toWorldCoords(v) {
    const pnt2 = this.localToGlobal.transformPoint(v);
    const newVec = new Vector(pnt2);
    pnt2.delete();
    return newVec;
  }
}
var PLANES_CONFIG = {
  XY: {
    xDir: [1, 0, 0],
    normal: [0, 0, 1]
  },
  YZ: {
    xDir: [0, 1, 0],
    normal: [1, 0, 0]
  },
  ZX: {
    xDir: [0, 0, 1],
    normal: [0, 1, 0]
  },
  XZ: {
    xDir: [1, 0, 0],
    normal: [0, -1, 0]
  },
  YX: {
    xDir: [0, 1, 0],
    normal: [0, 0, -1]
  },
  ZY: {
    xDir: [0, 0, 1],
    normal: [-1, 0, 0]
  },
  front: {
    xDir: [1, 0, 0],
    normal: [0, 0, 1]
  },
  back: {
    xDir: [-1, 0, 0],
    normal: [0, 0, -1]
  },
  left: {
    xDir: [0, 0, 1],
    normal: [-1, 0, 0]
  },
  right: {
    xDir: [0, 0, -1],
    normal: [1, 0, 0]
  },
  top: {
    xDir: [1, 0, 0],
    normal: [0, 1, 0]
  },
  bottom: {
    xDir: [1, 0, 0],
    normal: [0, -1, 0]
  }
};
var createNamedPlane = (plane, sourceOrigin = [0, 0, 0]) => {
  const config = PLANES_CONFIG[plane];
  if (!config)
    throw new Error(`Could not find plane ${plane}`);
  let origin;
  if (typeof sourceOrigin === "number") {
    origin = config.normal.map((v) => v * sourceOrigin);
  } else {
    origin = sourceOrigin;
  }
  return new Plane(origin, config.xDir, config.normal);
};
var BoundingBox$1 = class BoundingBox extends WrappingObj {
  constructor(wrapped) {
    const oc = getOC();
    let boundBox = wrapped;
    if (!boundBox) {
      boundBox = new oc.Bnd_Box_1;
    }
    super(boundBox);
  }
  static fromBounds(min, max) {
    const box = new BoundingBox;
    box.wrapped.Update(min[0], min[1], min[2], max[0], max[1], max[2]);
    return box;
  }
  get repr() {
    const [min, max] = this.bounds;
    return `${new Vector(min).repr} - ${new Vector(max).repr}`;
  }
  get bounds() {
    const xMin = { current: 0 };
    const yMin = { current: 0 };
    const zMin = { current: 0 };
    const xMax = { current: 0 };
    const yMax = { current: 0 };
    const zMax = { current: 0 };
    this.wrapped.Get(xMin, yMin, zMin, xMax, yMax, zMax);
    return [
      [xMin.current, yMin.current, zMin.current],
      [xMax.current, yMax.current, zMax.current]
    ];
  }
  get center() {
    const [[xmin, ymin, zmin], [xmax, ymax, zmax]] = this.bounds;
    return [
      xmin + (xmax - xmin) / 2,
      ymin + (ymax - ymin) / 2,
      zmin + (zmax - zmin) / 2
    ];
  }
  get width() {
    const [[xmin], [xmax]] = this.bounds;
    return Math.abs(xmax - xmin);
  }
  get height() {
    const [[, ymin], [, ymax]] = this.bounds;
    return Math.abs(ymax - ymin);
  }
  get depth() {
    const [[, , zmin], [, , zmax]] = this.bounds;
    return Math.abs(zmax - zmin);
  }
  add(other) {
    this.wrapped.Add_1(other.wrapped);
  }
  isOut(other) {
    return this.wrapped.IsOut_4(other.wrapped);
  }
};
function makePlane(plane = "XY", origin = [0, 0, 0]) {
  if (plane instanceof Plane) {
    return plane.clone();
  } else {
    return createNamedPlane(plane, origin);
  }
}
function rotate(shape, angle, position = [0, 0, 0], direction = [0, 0, 1]) {
  const transformation = new Transformation;
  transformation.rotate(angle, position, direction);
  const newShape = transformation.transform(shape);
  transformation.delete();
  return newShape;
}
function translate(shape, vector) {
  const transformation = new Transformation;
  transformation.translate(vector);
  const newShape = transformation.transform(shape);
  transformation.delete();
  return newShape;
}
function mirror(shape, inputPlane, origin) {
  const transformation = new Transformation;
  transformation.mirror(inputPlane, origin);
  const newShape = transformation.transform(shape);
  transformation.delete();
  return newShape;
}
function scale(shape, center, scale2) {
  const transformation = new Transformation;
  transformation.scale(center, scale2);
  const newShape = transformation.transform(shape);
  transformation.delete();
  return newShape;
}
function isPoint2D(point) {
  return Array.isArray(point) && point.length === 2;
}
var CURVE_TYPES_MAP = null;
var getCurveTypesMap = (refresh) => {
  if (CURVE_TYPES_MAP && !refresh)
    return CURVE_TYPES_MAP;
  const oc = getOC();
  const ga = oc.GeomAbs_CurveType;
  CURVE_TYPES_MAP = /* @__PURE__ */ new Map([
    [ga.GeomAbs_Line, "LINE"],
    [ga.GeomAbs_Circle, "CIRCLE"],
    [ga.GeomAbs_Ellipse, "ELLIPSE"],
    [ga.GeomAbs_Hyperbola, "HYPERBOLA"],
    [ga.GeomAbs_Parabola, "PARABOLA"],
    [ga.GeomAbs_BezierCurve, "BEZIER_CURVE"],
    [ga.GeomAbs_BSplineCurve, "BSPLINE_CURVE"],
    [ga.GeomAbs_OffsetCurve, "OFFSET_CURVE"],
    [ga.GeomAbs_OtherCurve, "OTHER_CURVE"]
  ]);
  return CURVE_TYPES_MAP;
};
var findCurveType = (type) => {
  let shapeType2 = getCurveTypesMap().get(type);
  if (!shapeType2)
    shapeType2 = getCurveTypesMap(true).get(type);
  if (!shapeType2)
    throw new Error("unknown type");
  return shapeType2;
};
function precisionRound(number, precision) {
  const factor = Math.pow(10, precision);
  const n = precision < 0 ? number : 0.01 / factor + number;
  return Math.round(n * factor) / factor;
}
function range(len) {
  return Array.from(Array(len).keys());
}
function zip(arrays) {
  const minLength = Math.min(...arrays.map((arr) => arr.length));
  return range(minLength).map((i) => arrays.map((arr) => arr[i]));
}
function round2(v) {
  return Math.round(v * 100) / 100;
}
var reprPnt = ([x, y]) => {
  return `(${round2(x)},${round2(y)})`;
};
var asFixed = (p, precision = 0.000000001) => {
  let num = p;
  if (Math.abs(p) < precision)
    num = 0;
  return num.toFixed(-Math.log10(precision));
};
var removeDuplicatePoints = (points, precision = 0.000000001) => {
  return Array.from(new Map(points.map(([p0, p1]) => [
    `[${asFixed(p0, precision)},${asFixed(p1, precision)}]`,
    [p0, p1]
  ])).values());
};
var pnt = ([x, y]) => {
  const oc = getOC();
  return new oc.gp_Pnt2d_3(x, y);
};
var direction2d = ([x, y]) => {
  const oc = getOC();
  return new oc.gp_Dir2d_4(x, y);
};
var vec = ([x, y]) => {
  const oc = getOC();
  return new oc.gp_Vec2d_4(x, y);
};
var axis2d = (point, direction) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const axis = new oc.gp_Ax2d_2(r(pnt(point)), r(direction2d(direction)));
  gc();
  return axis;
};

class BoundingBox2d extends WrappingObj {
  constructor(wrapped) {
    const oc = getOC();
    let boundBox = wrapped;
    if (!boundBox) {
      boundBox = new oc.Bnd_Box2d;
    }
    super(boundBox);
  }
  get repr() {
    const [min, max] = this.bounds;
    return `${reprPnt(min)} - ${reprPnt(max)}`;
  }
  get bounds() {
    const xMin = { current: 0 };
    const yMin = { current: 0 };
    const xMax = { current: 0 };
    const yMax = { current: 0 };
    this.wrapped.Get(xMin, yMin, xMax, yMax);
    return [
      [xMin.current, yMin.current],
      [xMax.current, yMax.current]
    ];
  }
  get center() {
    const [[xmin, ymin], [xmax, ymax]] = this.bounds;
    return [xmin + (xmax - xmin) / 2, ymin + (ymax - ymin) / 2];
  }
  get width() {
    const [[xmin], [xmax]] = this.bounds;
    return Math.abs(xmax - xmin);
  }
  get height() {
    const [[, ymin], [, ymax]] = this.bounds;
    return Math.abs(ymax - ymin);
  }
  outsidePoint(paddingPercent = 1) {
    const [min, max] = this.bounds;
    const width = max[0] - min[0];
    const height = max[1] - min[1];
    return [
      max[0] + width / 100 * paddingPercent,
      max[1] + height / 100 * paddingPercent * 0.9
    ];
  }
  add(other) {
    this.wrapped.Add_1(other.wrapped);
  }
  isOut(other) {
    return this.wrapped.IsOut_4(other.wrapped);
  }
  containsPoint(other) {
    const r = GCWithScope();
    const point = r(pnt(other));
    return !this.wrapped.IsOut_1(point);
  }
}
var samePoint$2 = ([x0, y0], [x1, y1], precision = 0.000001) => {
  return Math.abs(x0 - x1) <= precision && Math.abs(y0 - y1) <= precision;
};
var add2d = ([x0, y0], [x1, y1]) => {
  return [x0 + x1, y0 + y1];
};
var subtract2d = ([x0, y0], [x1, y1]) => {
  return [x0 - x1, y0 - y1];
};
var scalarMultiply2d = ([x0, y0], scalar) => {
  return [x0 * scalar, y0 * scalar];
};
var distance2d = ([x0, y0], [x1, y1] = [0, 0]) => {
  return Math.sqrt((x0 - x1) ** 2 + (y0 - y1) ** 2);
};
var squareDistance2d = ([x0, y0], [x1, y1] = [0, 0]) => {
  return (x0 - x1) ** 2 + (y0 - y1) ** 2;
};
function crossProduct2d([x0, y0], [x1, y1]) {
  return x0 * y1 - y0 * x1;
}
var angle2d = ([x0, y0], [x1, y1] = [0, 0]) => {
  return Math.atan2(y1 * x0 - y0 * x1, x0 * x1 + y0 * y1);
};
var polarAngle2d = ([x0, y0], [x1, y1] = [0, 0]) => {
  return Math.atan2(y1 - y0, x1 - x0);
};
var normalize2d = ([x0, y0]) => {
  const l = distance2d([x0, y0]);
  return [x0 / l, y0 / l];
};
var rotate2d = (point, angle, center = [0, 0]) => {
  const [px0, py0] = point;
  const [cx, cy] = center;
  const px = px0 - cx;
  const py = py0 - cy;
  const sinA = Math.sin(angle);
  const cosA = Math.cos(angle);
  const xnew = px * cosA - py * sinA;
  const ynew = px * sinA + py * cosA;
  return [xnew + cx, ynew + cy];
};
var polarToCartesian = (r, theta) => {
  const x = Math.cos(theta) * r;
  const y = Math.sin(theta) * r;
  return [x, y];
};
var cartesianToPolar = ([x, y]) => {
  const r = distance2d([x, y]);
  const theta = Math.atan2(y, x);
  return [r, theta];
};
var determinant2x2 = (matrix) => {
  return matrix[0][0] * matrix[1][1] - matrix[0][1] * matrix[1][0];
};
class Curve2D extends WrappingObj {
  constructor(handle) {
    const oc = getOC();
    const inner = handle.get();
    super(new oc.Handle_Geom2d_Curve_2(inner));
    this._boundingBox = null;
  }
  get boundingBox() {
    if (this._boundingBox)
      return this._boundingBox;
    const oc = getOC();
    const boundBox = new oc.Bnd_Box2d;
    oc.BndLib_Add2dCurve.Add_3(this.wrapped, 0.000001, boundBox);
    this._boundingBox = new BoundingBox2d(boundBox);
    return this._boundingBox;
  }
  get repr() {
    return `${this.geomType} ${reprPnt(this.firstPoint)} - ${reprPnt(this.lastPoint)}`;
  }
  get innerCurve() {
    return this.wrapped.get();
  }
  serialize() {
    const oc = getOC();
    return oc.GeomToolsWrapper.Write(this.wrapped);
  }
  value(parameter) {
    const pnt2 = this.innerCurve.Value(parameter);
    const vec2 = [pnt2.X(), pnt2.Y()];
    pnt2.delete();
    return vec2;
  }
  get firstPoint() {
    return this.value(this.firstParameter);
  }
  get lastPoint() {
    return this.value(this.lastParameter);
  }
  get firstParameter() {
    return this.innerCurve.FirstParameter();
  }
  get lastParameter() {
    return this.innerCurve.LastParameter();
  }
  adaptor() {
    const oc = getOC();
    return new oc.Geom2dAdaptor_Curve_2(this.wrapped);
  }
  get geomType() {
    const adaptor = this.adaptor();
    const curveType = findCurveType(adaptor.GetType());
    adaptor.delete();
    return curveType;
  }
  clone() {
    return new Curve2D(this.innerCurve.Copy());
  }
  reverse() {
    this.innerCurve.Reverse();
  }
  distanceFromPoint(point) {
    const oc = getOC();
    const r = GCWithScope();
    const projector = r(new oc.Geom2dAPI_ProjectPointOnCurve_2(r(pnt(point)), this.wrapped));
    let curveToPoint = Infinity;
    try {
      curveToPoint = projector.LowerDistance();
    } catch (e) {
      curveToPoint = Infinity;
    }
    return Math.min(curveToPoint, distance2d(point, this.firstPoint), distance2d(point, this.lastPoint));
  }
  distanceFromCurve(curve) {
    const oc = getOC();
    const r = GCWithScope();
    let curveDistance = Infinity;
    const projector = r(new oc.Geom2dAPI_ExtremaCurveCurve(this.wrapped, curve.wrapped, this.firstParameter, this.lastParameter, curve.firstParameter, curve.lastParameter));
    try {
      curveDistance = projector.LowerDistance();
    } catch (e) {
      curveDistance = Infinity;
    }
    return Math.min(curveDistance, this.distanceFromPoint(curve.firstPoint), this.distanceFromPoint(curve.lastPoint), curve.distanceFromPoint(this.firstPoint), curve.distanceFromPoint(this.lastPoint));
  }
  distanceFrom(element) {
    if (isPoint2D(element)) {
      return this.distanceFromPoint(element);
    }
    return this.distanceFromCurve(element);
  }
  isOnCurve(point) {
    return this.distanceFromPoint(point) < 0.000000001;
  }
  parameter(point, precision = 0.000000001) {
    const oc = getOC();
    const r = GCWithScope();
    let lowerDistance;
    let lowerDistanceParameter;
    try {
      const projector = r(new oc.Geom2dAPI_ProjectPointOnCurve_2(r(pnt(point)), this.wrapped));
      lowerDistance = projector.LowerDistance();
      lowerDistanceParameter = projector.LowerDistanceParameter();
    } catch (e) {
      if (samePoint$2(point, this.firstPoint, precision))
        return this.firstParameter;
      if (samePoint$2(point, this.lastPoint, precision))
        return this.lastParameter;
      throw new Error("Failed to find parameter");
    }
    if (lowerDistance > precision) {
      throw new Error(`Point ${reprPnt(point)} not on curve ${this.repr}, ${lowerDistance.toFixed(9)}`);
    }
    return lowerDistanceParameter;
  }
  tangentAt(index) {
    const oc = getOC();
    const [r, gc] = localGC();
    let param;
    if (Array.isArray(index)) {
      param = this.parameter(index);
    } else {
      const paramLength = this.innerCurve.LastParameter() - this.innerCurve.FirstParameter();
      param = paramLength * index + this.innerCurve.FirstParameter();
    }
    const point = r(new oc.gp_Pnt2d_1);
    const dir = r(new oc.gp_Vec2d_1);
    this.innerCurve.D1(param, point, dir);
    const tgtVec = [dir.X(), dir.Y()];
    gc();
    return tgtVec;
  }
  splitAt(points, precision = 0.000000001) {
    const oc = getOC();
    const r = GCWithScope();
    let parameters = points.map((point) => {
      if (isPoint2D(point))
        return this.parameter(point, precision);
      return point;
    });
    parameters = Array.from(new Map(parameters.map((p) => [precisionRound(p, -Math.log10(precision)), p])).values()).sort((a, b) => a - b);
    const firstParam = this.firstParameter;
    const lastParam = this.lastParameter;
    if (firstParam > lastParam) {
      parameters.reverse();
    }
    if (Math.abs(parameters[0] - firstParam) < precision * 100)
      parameters = parameters.slice(1);
    if (!parameters.length)
      return [this];
    if (Math.abs(parameters[parameters.length - 1] - lastParam) < precision * 100)
      parameters = parameters.slice(0, -1);
    if (!parameters.length)
      return [this];
    return zip([
      [firstParam, ...parameters],
      [...parameters, lastParam]
    ]).map(([first, last]) => {
      try {
        if (this.geomType === "BEZIER_CURVE") {
          const curveCopy = new oc.Geom2d_BezierCurve_1(r(this.adaptor()).Bezier().get().Poles_2());
          curveCopy.Segment(first, last);
          return new Curve2D(new oc.Handle_Geom2d_Curve_2(curveCopy));
        }
        if (this.geomType === "BSPLINE_CURVE") {
          const adapted = r(this.adaptor()).BSpline().get();
          const curveCopy = new oc.Geom2d_BSplineCurve_1(adapted.Poles_2(), adapted.Knots_2(), adapted.Multiplicities_2(), adapted.Degree(), adapted.IsPeriodic());
          curveCopy.Segment(first, last, precision);
          return new Curve2D(new oc.Handle_Geom2d_Curve_2(curveCopy));
        }
        const trimmed = new oc.Geom2d_TrimmedCurve(this.wrapped, first, last, true, true);
        return new Curve2D(new oc.Handle_Geom2d_Curve_2(trimmed));
      } catch (e) {
        throw new Error("Failed to split the curve");
      }
    });
  }
}
var approximateAsBSpline = (adaptor, tolerance = 0.0001, continuity = "C0", maxSegments = 200) => {
  const oc = getOC();
  const r = GCWithScope();
  const continuities = {
    C0: oc.GeomAbs_Shape.GeomAbs_C0,
    C1: oc.GeomAbs_Shape.GeomAbs_C1,
    C2: oc.GeomAbs_Shape.GeomAbs_C2,
    C3: oc.GeomAbs_Shape.GeomAbs_C3
  };
  const convert = r(new oc.Geom2dConvert_ApproxCurve_2(adaptor.ShallowCopy(), tolerance, continuities[continuity], maxSegments, 3));
  return new Curve2D(convert.Curve());
};
var BSplineToBezier = (adaptor) => {
  if (findCurveType(adaptor.GetType()) !== "BSPLINE_CURVE")
    throw new Error("You can only convert a Bspline");
  const handle = adaptor.BSpline();
  const firstParameter = adaptor.FirstParameter();
  const lastParameter = adaptor.LastParameter();
  const oc = getOC();
  const convert = new oc.Geom2dConvert_BSplineCurveToBezierCurve_2(handle, firstParameter, lastParameter, 0.000000001);
  function* bezierCurves() {
    const nArcs = convert.NbArcs();
    if (!nArcs)
      return;
    for (let i = 1;i <= nArcs; i++) {
      const arc = convert.Arc(i);
      yield new Curve2D(arc);
    }
  }
  const curves = Array.from(bezierCurves());
  convert.delete();
  return curves;
};
function approximateAsSvgCompatibleCurve(curves, options = {
  tolerance: 0.0001,
  continuity: "C0",
  maxSegments: 300
}) {
  const r = GCWithScope();
  return curves.flatMap((curve) => {
    const adaptor = r(curve.adaptor());
    const curveType = findCurveType(adaptor.GetType());
    const midpointParameter = 0.5 * (curve.firstParameter + curve.lastParameter);
    if (curveType === "ELLIPSE" || curveType === "CIRCLE" && samePoint$2(curve.firstPoint, curve.lastPoint)) {
      return curve.splitAt([midpointParameter]);
    }
    if (["LINE", "ELLIPSE", "CIRCLE"].includes(curveType)) {
      return curve;
    }
    if (curveType === "BEZIER_CURVE") {
      const b = adaptor.Bezier().get();
      const deg = b.Degree();
      if (!b.IsRational() && [1, 2, 3].includes(deg)) {
        return curve;
      }
    }
    if (curveType === "BSPLINE_CURVE") {
      const bspline2 = adaptor.BSpline().get();
      if (!bspline2.IsRational()) {
        const c = BSplineToBezier(adaptor);
        return approximateAsSvgCompatibleCurve(c, options);
      }
    }
    const bspline = approximateAsBSpline(adaptor, options.tolerance, options.continuity, options.maxSegments);
    return approximateAsSvgCompatibleCurve(BSplineToBezier(r(bspline.adaptor())), options);
  });
}
function* pointsIteration(intersector) {
  const nPoints = intersector.NbPoints();
  if (!nPoints)
    return;
  for (let i = 1;i <= nPoints; i++) {
    const point = intersector.Point(i);
    yield [point.X(), point.Y()];
  }
}
function* commonSegmentsIteration(intersector) {
  const nSegments = intersector.NbSegments();
  if (!nSegments)
    return;
  const oc = getOC();
  for (let i = 1;i <= nSegments; i++) {
    const h1 = new oc.Handle_Geom2d_Curve_1;
    const h2 = new oc.Handle_Geom2d_Curve_1;
    try {
      intersector.Segment(i, h1, h2);
    } catch (e) {
      continue;
    }
    yield new Curve2D(h1);
    h2.delete();
  }
}
var intersectCurves = (first, second, precision = 0.000000001) => {
  if (first.boundingBox.isOut(second.boundingBox))
    return { intersections: [], commonSegments: [], commonSegmentsPoints: [] };
  const oc = getOC();
  const intersector = new oc.Geom2dAPI_InterCurveCurve_1;
  let intersections;
  let commonSegments;
  try {
    intersector.Init_1(first.wrapped, second.wrapped, precision);
    intersections = Array.from(pointsIteration(intersector));
    commonSegments = Array.from(commonSegmentsIteration(intersector));
  } catch (e) {
    console.error(first, second, e);
    throw new Error("Intersections failed between curves");
  } finally {
    intersector.delete();
  }
  const segmentsAsPoints = commonSegments.filter((c) => samePoint$2(c.firstPoint, c.lastPoint, precision)).map((c) => c.firstPoint);
  if (segmentsAsPoints.length) {
    intersections.push(...segmentsAsPoints);
    commonSegments = commonSegments.filter((c) => !samePoint$2(c.firstPoint, c.lastPoint, precision));
  }
  const commonSegmentsPoints = commonSegments.flatMap((c) => [
    c.firstPoint,
    c.lastPoint
  ]);
  return { intersections, commonSegments, commonSegmentsPoints };
};
var selfIntersections = (curve, precision = 0.000000001) => {
  const oc = getOC();
  const intersector = new oc.Geom2dAPI_InterCurveCurve_1;
  let intersections;
  try {
    intersector.Init_1(curve.wrapped, curve.wrapped, precision);
    intersections = Array.from(pointsIteration(intersector));
  } catch (e) {
    throw new Error("Self intersection failed");
  } finally {
    intersector.delete();
  }
  return intersections;
};
var make2dSegmentCurve = (startPoint, endPoint) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const segment = r(new oc.GCE2d_MakeSegment_1(r(pnt(startPoint)), r(pnt(endPoint)))).Value();
  const curve = new Curve2D(segment);
  if (!samePoint$2(curve.firstPoint, startPoint)) {
    curve.reverse();
  }
  gc();
  return curve;
};
var make2dThreePointArc = (startPoint, midPoint, endPoint) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const segment = r(new oc.GCE2d_MakeArcOfCircle_4(r(pnt(startPoint)), r(pnt(midPoint)), r(pnt(endPoint)))).Value();
  gc();
  const curve = new Curve2D(segment);
  if (!samePoint$2(curve.firstPoint, startPoint)) {
    curve.wrapped.get().SetTrim(curve.lastParameter, curve.firstParameter, true, true);
  }
  return curve;
};
var make2dTangentArc = (startPoint, tangent, endPoint) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const segment = r(new oc.GCE2d_MakeArcOfCircle_5(r(pnt(startPoint)), r(vec(tangent)), r(pnt(endPoint)))).Value();
  gc();
  const curve = new Curve2D(segment);
  if (!samePoint$2(curve.firstPoint, startPoint)) {
    curve.wrapped.get().SetTrim(curve.lastParameter, curve.firstParameter, true, true);
  }
  return curve;
};
var make2dCircle = (radius, center = [0, 0]) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const segment = r(new oc.GCE2d_MakeCircle_7(r(pnt(center)), radius, true)).Value();
  gc();
  return new Curve2D(segment);
};
var make2dEllipseArc = (majorRadius, minorRadius, startAngle, endAngle, center = [0, 0], xDir, direct = true) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const ellipse = r(new oc.gp_Elips2d_2(r(axis2d(center, xDir)), majorRadius, minorRadius, true));
  const segment = r(new oc.GCE2d_MakeArcOfEllipse_1(ellipse, startAngle, endAngle, direct)).Value();
  gc();
  return new Curve2D(segment);
};
var make2dBezierCurve = (startPoint, controls, endPoint) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const arrayOfPoints = r(new oc.TColgp_Array1OfPnt2d_2(1, controls.length + 2));
  arrayOfPoints.SetValue(1, r(pnt(startPoint)));
  controls.forEach((p, i) => {
    arrayOfPoints.SetValue(i + 2, r(pnt(p)));
  });
  arrayOfPoints.SetValue(controls.length + 2, r(pnt(endPoint)));
  const bezCurve = new oc.Geom2d_BezierCurve_1(arrayOfPoints);
  gc();
  return new Curve2D(new oc.Handle_Geom2d_Curve_2(bezCurve));
};
var make2dArcFromCenter = (startPoint, endPoint, center, longArc = false) => {
  const midChord = scalarMultiply2d(add2d(startPoint, endPoint), 0.5);
  const orientedRadius = distance2d(center, startPoint) * (longArc ? -1 : 1);
  const midChordDir = normalize2d(subtract2d(midChord, center));
  return make2dThreePointArc(startPoint, add2d(scalarMultiply2d(midChordDir, orientedRadius), center), endPoint);
};
var offsetEndPoints = (firstPoint, lastPoint, offset2) => {
  const tangent = normalize2d(subtract2d(lastPoint, firstPoint));
  const normal = [tangent[1], -tangent[0]];
  const offsetVec = [normal[0] * offset2, normal[1] * offset2];
  return {
    firstPoint: add2d(firstPoint, offsetVec),
    lastPoint: add2d(lastPoint, offsetVec)
  };
};
var make2dOffset = (curve, offset2) => {
  const r = GCWithScope();
  const curveType = curve.geomType;
  if (curveType === "CIRCLE") {
    const circle = r(r(curve.adaptor()).Circle());
    const radius = circle.Radius();
    const orientationCorrection = circle.IsDirect() ? 1 : -1;
    const orientedOffset = offset2 * orientationCorrection;
    const newRadius = radius + orientedOffset;
    if (newRadius < 0.0000000001) {
      const centerPos = r(circle.Location());
      const center = [centerPos.X(), centerPos.Y()];
      const offsetViaCenter = (point) => {
        const [x, y] = normalize2d(subtract2d(point, center));
        return add2d(point, [orientedOffset * x, orientedOffset * y]);
      };
      return {
        collapsed: true,
        firstPoint: offsetViaCenter(curve.firstPoint),
        lastPoint: offsetViaCenter(curve.lastPoint)
      };
    }
    const oc2 = getOC();
    const newCircle = new oc2.gp_Circ2d_3(circle.Axis(), newRadius);
    const newInnerCurve = new oc2.Geom2d_Circle_1(newCircle);
    const newCurve = new oc2.Geom2d_TrimmedCurve(new oc2.Handle_Geom2d_Curve_2(newInnerCurve), curve.firstParameter, curve.lastParameter, true, true);
    return new Curve2D(new oc2.Handle_Geom2d_Curve_2(newCurve));
  }
  if (curveType === "LINE") {
    const { firstPoint, lastPoint } = offsetEndPoints(curve.firstPoint, curve.lastPoint, offset2);
    return make2dSegmentCurve(firstPoint, lastPoint);
  }
  const oc = getOC();
  const offsetCurve = new Curve2D(new oc.Handle_Geom2d_Curve_2(new oc.Geom2d_OffsetCurve(curve.wrapped, offset2, true)));
  const approximation = approximateAsBSpline(offsetCurve.adaptor());
  const selfIntersects = selfIntersections(approximation);
  if (selfIntersects.length) {
    return {
      collapsed: true,
      firstPoint: approximation.firstPoint,
      lastPoint: approximation.lastPoint
    };
  }
  return approximation;
};
function removeCorner(firstCurve, secondCurve, radius) {
  const sinAngle = crossProduct2d(firstCurve.tangentAt(1), secondCurve.tangentAt(0));
  if (Math.abs(sinAngle) < 0.0000000001)
    return null;
  const orientationCorrection = sinAngle > 0 ? -1 : 1;
  const offset2 = Math.abs(radius) * orientationCorrection;
  const firstOffset = make2dOffset(firstCurve, offset2);
  const secondOffset = make2dOffset(secondCurve, offset2);
  if (!(firstOffset instanceof Curve2D) || !(secondOffset instanceof Curve2D)) {
    return null;
  }
  let potentialCenter;
  try {
    const { intersections } = intersectCurves(firstOffset, secondOffset, 0.000000001);
    potentialCenter = intersections.at(-1);
  } catch (e) {
    return null;
  }
  if (!isPoint2D(potentialCenter)) {
    return null;
  }
  const center = potentialCenter;
  const splitForFillet = (curve, offsetCurve) => {
    const [x, y] = offsetCurve.tangentAt(center);
    const normal = normalize2d([-y, x]);
    const splitPoint = add2d(center, scalarMultiply2d(normal, offset2));
    const splitParam = curve.parameter(splitPoint, 0.000001);
    return curve.splitAt([splitParam]);
  };
  const [first] = splitForFillet(firstCurve, firstOffset);
  const [, second] = splitForFillet(secondCurve, secondOffset);
  return { first, second, center };
}
function filletCurves(firstCurve, secondCurve, radius) {
  const cornerRemoved = removeCorner(firstCurve, secondCurve, radius);
  if (!cornerRemoved) {
    console.warn("Cannot fillet between curves", firstCurve.repr, secondCurve.repr);
    return [firstCurve, secondCurve];
  }
  const { first, second, center } = cornerRemoved;
  return [
    first,
    make2dArcFromCenter(first.lastPoint, second.firstPoint, center),
    second
  ];
}
function chamferCurves(firstCurve, secondCurve, radius) {
  const cornerRemoved = removeCorner(firstCurve, secondCurve, radius);
  if (!cornerRemoved) {
    console.warn("Cannot chamfer between curves", firstCurve.repr, secondCurve.repr);
    return [firstCurve, secondCurve];
  }
  const { first, second } = cornerRemoved;
  return [
    first,
    make2dSegmentCurve(first.lastPoint, second.firstPoint),
    second
  ];
}
function dogboneFilletCurves(firstCurve, secondCurve, radius) {
  const tgt1 = normalize2d(firstCurve.tangentAt(1));
  const tgt2 = normalize2d(secondCurve.tangentAt(0));
  const sinAngle = crossProduct2d(tgt1, tgt2);
  const a = Math.asin(sinAngle);
  if (Math.abs(sinAngle) < 0.0000000001)
    return [firstCurve, secondCurve];
  const orientationCorrection = sinAngle > 0 ? -1 : 1;
  const offset2 = Math.abs(radius) * Math.sin(a / 2) * orientationCorrection;
  const firstOffset = make2dOffset(firstCurve, offset2);
  const secondOffset = make2dOffset(secondCurve, offset2);
  if (!(firstOffset instanceof Curve2D) || !(secondOffset instanceof Curve2D)) {
    return [firstCurve, secondCurve];
  }
  let potentialCenter;
  try {
    const { intersections } = intersectCurves(firstOffset, secondOffset, 0.000000001);
    potentialCenter = intersections.at(-1);
  } catch (e) {
    return [firstCurve, secondCurve];
  }
  if (!isPoint2D(potentialCenter)) {
    return [firstCurve, secondCurve];
  }
  const circle = make2dCircle(radius, potentialCenter);
  const firstInt = intersectCurves(firstCurve, circle).intersections[0];
  const secondInt = intersectCurves(secondCurve, circle).intersections.at(-1);
  if (!firstInt || !secondInt)
    return [firstCurve, secondCurve];
  const firstPart = firstCurve.splitAt([firstInt])[0];
  const secondPart = secondCurve.splitAt([secondInt]).at(-1);
  if (!firstPart || !secondPart) {
    return [firstCurve, secondCurve];
  }
  try {
    return [
      firstPart,
      make2dThreePointArc(firstPart.lastPoint, firstCurve.lastPoint, secondPart.firstPoint),
      secondPart
    ];
  } catch (e) {
    return [firstCurve, secondCurve];
  }
}

class FlatQueue {
  constructor() {
    this.ids = [];
    this.values = [];
    this.length = 0;
  }
  clear() {
    this.length = 0;
  }
  push(id, value) {
    let pos = this.length++;
    while (pos > 0) {
      const parent = pos - 1 >> 1;
      const parentValue = this.values[parent];
      if (value >= parentValue)
        break;
      this.ids[pos] = this.ids[parent];
      this.values[pos] = parentValue;
      pos = parent;
    }
    this.ids[pos] = id;
    this.values[pos] = value;
  }
  pop() {
    if (this.length === 0)
      return;
    const top = this.ids[0];
    this.length--;
    if (this.length > 0) {
      const id = this.ids[0] = this.ids[this.length];
      const value = this.values[0] = this.values[this.length];
      const halfLength = this.length >> 1;
      let pos = 0;
      while (pos < halfLength) {
        let left = (pos << 1) + 1;
        const right = left + 1;
        let bestIndex = this.ids[left];
        let bestValue = this.values[left];
        const rightValue = this.values[right];
        if (right < this.length && rightValue < bestValue) {
          left = right;
          bestIndex = this.ids[right];
          bestValue = rightValue;
        }
        if (bestValue >= value)
          break;
        this.ids[pos] = bestIndex;
        this.values[pos] = bestValue;
        pos = left;
      }
      this.ids[pos] = id;
      this.values[pos] = value;
    }
    return top;
  }
  peek() {
    if (this.length === 0)
      return;
    return this.ids[0];
  }
  peekValue() {
    if (this.length === 0)
      return;
    return this.values[0];
  }
  shrink() {
    this.ids.length = this.values.length = this.length;
  }
}
var ARRAY_TYPES = [
  Int8Array,
  Uint8Array,
  Uint8ClampedArray,
  Int16Array,
  Uint16Array,
  Int32Array,
  Uint32Array,
  Float32Array,
  Float64Array
];
var VERSION = 3;

class Flatbush {
  static from(data) {
    if (!(data instanceof ArrayBuffer)) {
      throw new Error("Data must be an instance of ArrayBuffer.");
    }
    const [magic, versionAndType] = new Uint8Array(data, 0, 2);
    if (magic !== 251) {
      throw new Error("Data does not appear to be in a Flatbush format.");
    }
    if (versionAndType >> 4 !== VERSION) {
      throw new Error(`Got v${versionAndType >> 4} data when expected v${VERSION}.`);
    }
    const [nodeSize] = new Uint16Array(data, 2, 1);
    const [numItems] = new Uint32Array(data, 4, 1);
    return new Flatbush(numItems, nodeSize, ARRAY_TYPES[versionAndType & 15], data);
  }
  constructor(numItems, nodeSize = 16, ArrayType = Float64Array, data) {
    if (numItems === undefined)
      throw new Error("Missing required argument: numItems.");
    if (isNaN(numItems) || numItems <= 0)
      throw new Error(`Unpexpected numItems value: ${numItems}.`);
    this.numItems = +numItems;
    this.nodeSize = Math.min(Math.max(+nodeSize, 2), 65535);
    let n = numItems;
    let numNodes = n;
    this._levelBounds = [n * 4];
    do {
      n = Math.ceil(n / this.nodeSize);
      numNodes += n;
      this._levelBounds.push(numNodes * 4);
    } while (n !== 1);
    this.ArrayType = ArrayType || Float64Array;
    this.IndexArrayType = numNodes < 16384 ? Uint16Array : Uint32Array;
    const arrayTypeIndex = ARRAY_TYPES.indexOf(this.ArrayType);
    const nodesByteSize = numNodes * 4 * this.ArrayType.BYTES_PER_ELEMENT;
    if (arrayTypeIndex < 0) {
      throw new Error(`Unexpected typed array class: ${ArrayType}.`);
    }
    if (data && data instanceof ArrayBuffer) {
      this.data = data;
      this._boxes = new this.ArrayType(this.data, 8, numNodes * 4);
      this._indices = new this.IndexArrayType(this.data, 8 + nodesByteSize, numNodes);
      this._pos = numNodes * 4;
      this.minX = this._boxes[this._pos - 4];
      this.minY = this._boxes[this._pos - 3];
      this.maxX = this._boxes[this._pos - 2];
      this.maxY = this._boxes[this._pos - 1];
    } else {
      this.data = new ArrayBuffer(8 + nodesByteSize + numNodes * this.IndexArrayType.BYTES_PER_ELEMENT);
      this._boxes = new this.ArrayType(this.data, 8, numNodes * 4);
      this._indices = new this.IndexArrayType(this.data, 8 + nodesByteSize, numNodes);
      this._pos = 0;
      this.minX = Infinity;
      this.minY = Infinity;
      this.maxX = -Infinity;
      this.maxY = -Infinity;
      new Uint8Array(this.data, 0, 2).set([251, (VERSION << 4) + arrayTypeIndex]);
      new Uint16Array(this.data, 2, 1)[0] = nodeSize;
      new Uint32Array(this.data, 4, 1)[0] = numItems;
    }
    this._queue = new FlatQueue;
  }
  add(minX, minY, maxX, maxY) {
    const index = this._pos >> 2;
    this._indices[index] = index;
    this._boxes[this._pos++] = minX;
    this._boxes[this._pos++] = minY;
    this._boxes[this._pos++] = maxX;
    this._boxes[this._pos++] = maxY;
    if (minX < this.minX)
      this.minX = minX;
    if (minY < this.minY)
      this.minY = minY;
    if (maxX > this.maxX)
      this.maxX = maxX;
    if (maxY > this.maxY)
      this.maxY = maxY;
    return index;
  }
  finish() {
    if (this._pos >> 2 !== this.numItems) {
      throw new Error(`Added ${this._pos >> 2} items when expected ${this.numItems}.`);
    }
    if (this.numItems <= this.nodeSize) {
      this._boxes[this._pos++] = this.minX;
      this._boxes[this._pos++] = this.minY;
      this._boxes[this._pos++] = this.maxX;
      this._boxes[this._pos++] = this.maxY;
      return;
    }
    const width = this.maxX - this.minX || 1;
    const height = this.maxY - this.minY || 1;
    const hilbertValues = new Uint32Array(this.numItems);
    const hilbertMax = (1 << 16) - 1;
    for (let i = 0;i < this.numItems; i++) {
      let pos = 4 * i;
      const minX = this._boxes[pos++];
      const minY = this._boxes[pos++];
      const maxX = this._boxes[pos++];
      const maxY = this._boxes[pos++];
      const x = Math.floor(hilbertMax * ((minX + maxX) / 2 - this.minX) / width);
      const y = Math.floor(hilbertMax * ((minY + maxY) / 2 - this.minY) / height);
      hilbertValues[i] = hilbert(x, y);
    }
    sort(hilbertValues, this._boxes, this._indices, 0, this.numItems - 1, this.nodeSize);
    for (let i = 0, pos = 0;i < this._levelBounds.length - 1; i++) {
      const end = this._levelBounds[i];
      while (pos < end) {
        const nodeIndex = pos;
        let nodeMinX = Infinity;
        let nodeMinY = Infinity;
        let nodeMaxX = -Infinity;
        let nodeMaxY = -Infinity;
        for (let i2 = 0;i2 < this.nodeSize && pos < end; i2++) {
          nodeMinX = Math.min(nodeMinX, this._boxes[pos++]);
          nodeMinY = Math.min(nodeMinY, this._boxes[pos++]);
          nodeMaxX = Math.max(nodeMaxX, this._boxes[pos++]);
          nodeMaxY = Math.max(nodeMaxY, this._boxes[pos++]);
        }
        this._indices[this._pos >> 2] = nodeIndex;
        this._boxes[this._pos++] = nodeMinX;
        this._boxes[this._pos++] = nodeMinY;
        this._boxes[this._pos++] = nodeMaxX;
        this._boxes[this._pos++] = nodeMaxY;
      }
    }
  }
  search(minX, minY, maxX, maxY, filterFn) {
    if (this._pos !== this._boxes.length) {
      throw new Error("Data not yet indexed - call index.finish().");
    }
    let nodeIndex = this._boxes.length - 4;
    const queue = [];
    const results = [];
    while (nodeIndex !== undefined) {
      const end = Math.min(nodeIndex + this.nodeSize * 4, upperBound(nodeIndex, this._levelBounds));
      for (let pos = nodeIndex;pos < end; pos += 4) {
        if (maxX < this._boxes[pos])
          continue;
        if (maxY < this._boxes[pos + 1])
          continue;
        if (minX > this._boxes[pos + 2])
          continue;
        if (minY > this._boxes[pos + 3])
          continue;
        const index = this._indices[pos >> 2] | 0;
        if (nodeIndex < this.numItems * 4) {
          if (filterFn === undefined || filterFn(index)) {
            results.push(index);
          }
        } else {
          queue.push(index);
        }
      }
      nodeIndex = queue.pop();
    }
    return results;
  }
  neighbors(x, y, maxResults = Infinity, maxDistance = Infinity, filterFn) {
    if (this._pos !== this._boxes.length) {
      throw new Error("Data not yet indexed - call index.finish().");
    }
    let nodeIndex = this._boxes.length - 4;
    const q = this._queue;
    const results = [];
    const maxDistSquared = maxDistance * maxDistance;
    while (nodeIndex !== undefined) {
      const end = Math.min(nodeIndex + this.nodeSize * 4, upperBound(nodeIndex, this._levelBounds));
      for (let pos = nodeIndex;pos < end; pos += 4) {
        const index = this._indices[pos >> 2] | 0;
        const dx = axisDist(x, this._boxes[pos], this._boxes[pos + 2]);
        const dy = axisDist(y, this._boxes[pos + 1], this._boxes[pos + 3]);
        const dist = dx * dx + dy * dy;
        if (nodeIndex < this.numItems * 4) {
          if (filterFn === undefined || filterFn(index)) {
            q.push((index << 1) + 1, dist);
          }
        } else {
          q.push(index << 1, dist);
        }
      }
      while (q.length && q.peek() & 1) {
        const dist = q.peekValue();
        if (dist > maxDistSquared) {
          q.clear();
          return results;
        }
        results.push(q.pop() >> 1);
        if (results.length === maxResults) {
          q.clear();
          return results;
        }
      }
      nodeIndex = q.pop() >> 1;
    }
    q.clear();
    return results;
  }
}
function axisDist(k, min, max) {
  return k < min ? min - k : k <= max ? 0 : k - max;
}
function upperBound(value, arr) {
  let i = 0;
  let j = arr.length - 1;
  while (i < j) {
    const m = i + j >> 1;
    if (arr[m] > value) {
      j = m;
    } else {
      i = m + 1;
    }
  }
  return arr[i];
}
function sort(values, boxes, indices, left, right, nodeSize) {
  if (Math.floor(left / nodeSize) >= Math.floor(right / nodeSize))
    return;
  const pivot = values[left + right >> 1];
  let i = left - 1;
  let j = right + 1;
  while (true) {
    do
      i++;
    while (values[i] < pivot);
    do
      j--;
    while (values[j] > pivot);
    if (i >= j)
      break;
    swap(values, boxes, indices, i, j);
  }
  sort(values, boxes, indices, left, j, nodeSize);
  sort(values, boxes, indices, j + 1, right, nodeSize);
}
function swap(values, boxes, indices, i, j) {
  const temp = values[i];
  values[i] = values[j];
  values[j] = temp;
  const k = 4 * i;
  const m = 4 * j;
  const a = boxes[k];
  const b = boxes[k + 1];
  const c = boxes[k + 2];
  const d = boxes[k + 3];
  boxes[k] = boxes[m];
  boxes[k + 1] = boxes[m + 1];
  boxes[k + 2] = boxes[m + 2];
  boxes[k + 3] = boxes[m + 3];
  boxes[m] = a;
  boxes[m + 1] = b;
  boxes[m + 2] = c;
  boxes[m + 3] = d;
  const e = indices[i];
  indices[i] = indices[j];
  indices[j] = e;
}
function hilbert(x, y) {
  let a = x ^ y;
  let b = 65535 ^ a;
  let c = 65535 ^ (x | y);
  let d = x & (y ^ 65535);
  let A = a | b >> 1;
  let B = a >> 1 ^ a;
  let C = c >> 1 ^ b & d >> 1 ^ c;
  let D = a & c >> 1 ^ d >> 1 ^ d;
  a = A;
  b = B;
  c = C;
  d = D;
  A = a & a >> 2 ^ b & b >> 2;
  B = a & b >> 2 ^ b & (a ^ b) >> 2;
  C ^= a & c >> 2 ^ b & d >> 2;
  D ^= b & c >> 2 ^ (a ^ b) & d >> 2;
  a = A;
  b = B;
  c = C;
  d = D;
  A = a & a >> 4 ^ b & b >> 4;
  B = a & b >> 4 ^ b & (a ^ b) >> 4;
  C ^= a & c >> 4 ^ b & d >> 4;
  D ^= b & c >> 4 ^ (a ^ b) & d >> 4;
  a = A;
  b = B;
  c = C;
  d = D;
  C ^= a & c >> 8 ^ b & d >> 8;
  D ^= b & c >> 8 ^ (a ^ b) & d >> 8;
  a = C ^ C >> 1;
  b = D ^ D >> 1;
  let i0 = x ^ y;
  let i1 = b | 65535 ^ (i0 | a);
  i0 = (i0 | i0 << 8) & 16711935;
  i0 = (i0 | i0 << 4) & 252645135;
  i0 = (i0 | i0 << 2) & 858993459;
  i0 = (i0 | i0 << 1) & 1431655765;
  i1 = (i1 | i1 << 8) & 16711935;
  i1 = (i1 | i1 << 4) & 252645135;
  i1 = (i1 | i1 << 2) & 858993459;
  i1 = (i1 | i1 << 1) & 1431655765;
  return (i1 << 1 | i0) >>> 0;
}
var stitchCurves = (curves, precision = 0.0000001) => {
  const startPoints = new Flatbush(curves.length);
  curves.forEach((c) => {
    const [x, y] = c.firstPoint;
    startPoints.add(x - precision, y - precision, x + precision, y + precision);
  });
  startPoints.finish();
  const stitchedCurves = [];
  const visited = /* @__PURE__ */ new Set;
  curves.forEach((curve, index) => {
    if (visited.has(index))
      return;
    const connectedCurves = [curve];
    let currentIndex = index;
    visited.add(index);
    let maxLoops = curves.length;
    while (true) {
      if (maxLoops-- < 0) {
        throw new Error("Infinite loop detected");
      }
      const lastPoint = connectedCurves[connectedCurves.length - 1].lastPoint;
      const [x, y] = lastPoint;
      const neighbors = startPoints.search(x - precision, y - precision, x + precision, y + precision);
      const indexDistance = (otherIndex) => Math.abs((currentIndex - otherIndex) % curves.length);
      const potentialNextCurves = neighbors.filter((neighborIndex) => !visited.has(neighborIndex)).map((neighborIndex) => [
        curves[neighborIndex],
        neighborIndex,
        indexDistance(neighborIndex)
      ]).sort(([, , a], [, , b]) => indexDistance(a) - indexDistance(b));
      if (potentialNextCurves.length === 0) {
        stitchedCurves.push(connectedCurves);
        break;
      }
      const [nextCurve, nextCurveIndex] = potentialNextCurves[0];
      connectedCurves.push(nextCurve);
      visited.add(nextCurveIndex);
      currentIndex = nextCurveIndex;
    }
  });
  return stitchedCurves;
};
function round5(v) {
  return Math.round(v * 1e5) / 1e5;
}
var fromPnt = (pnt2) => `${round5(pnt2.X())} ${round5(pnt2.Y())}`;
var adaptedCurveToPathElem = (adaptor, lastPoint) => {
  const oc = getOC();
  const curveType = findCurveType(adaptor.GetType());
  const [endX, endY] = lastPoint;
  const endpoint = `${round5(endX)} ${round5(endY)}`;
  if (curveType === "LINE") {
    return `L ${endpoint}`;
  }
  if (curveType === "BEZIER_CURVE") {
    const curve = adaptor.Bezier().get();
    const deg = curve.Degree();
    if (deg === 1) {
      return `L ${endpoint}`;
    }
    if (deg === 2) {
      return `Q ${fromPnt(curve.Pole(2))} ${endpoint}`;
    }
    if (deg === 3) {
      const p1 = fromPnt(curve.Pole(2));
      const p2 = fromPnt(curve.Pole(3));
      return `C ${p1} ${p2} ${endpoint}`;
    }
  }
  if (curveType === "CIRCLE") {
    const curve = adaptor.Circle();
    const radius = curve.Radius();
    const p1 = adaptor.FirstParameter();
    const p2 = adaptor.LastParameter();
    const paramAngle = (p2 - p1) * RAD2DEG;
    const end = paramAngle !== 360 ? endpoint : `${round5(endX)} ${round5(endY + 0.0001)}`;
    return `A ${radius} ${radius} 0 ${Math.abs(paramAngle) > 180 ? "1" : "0"} ${curve.IsDirect() ? "1" : "0"} ${end}`;
  }
  if (curveType === "ELLIPSE") {
    const curve = adaptor.Ellipse();
    const rx = curve.MajorRadius();
    const ry = curve.MinorRadius();
    const p1 = adaptor.FirstParameter();
    const p2 = adaptor.LastParameter();
    const paramAngle = (p2 - p1) * RAD2DEG;
    const end = paramAngle !== 360 ? endpoint : `${round5(endX)} ${round5(endY + 0.0001)}`;
    const dir0 = new oc.gp_Dir2d_1;
    const angle = 180 - curve.XAxis().Direction().Angle(dir0) * RAD2DEG;
    dir0.delete();
    return `A ${round5(rx)} ${round5(ry)} ${round5(angle)} ${Math.abs(paramAngle) > 180 ? "1" : "0"} ${curve.IsDirect() ? "1" : "0"} ${end}`;
  }
  throw new Error(`Unsupported curve type: ${curveType}`);
};
var MANIFOLD = {
  library: null
};
var getManifold = () => {
  if (!MANIFOLD.library)
    throw new Error("manifold has not been loaded");
  return MANIFOLD.library;
};
var asVec3 = (v) => {
  const vec2 = new Vector(v);
  const res = [vec2.x, vec2.y, vec2.z];
  return res;
};
var manifoldTransformFromTransformation = (t) => {
  const transform = t.inverted();
  const origin = transform.transformPoint([0, 0, 0]);
  const xPoint = transform.transformPoint([1, 0, 0]);
  const yPoint = transform.transformPoint([0, 1, 0]);
  const zPoint = transform.transformPoint([0, 0, 1]);
  const o = [origin.X(), origin.Y(), origin.Z()];
  const x = [xPoint.X() - o[0], xPoint.Y() - o[1], xPoint.Z() - o[2]];
  const y = [yPoint.X() - o[0], yPoint.Y() - o[1], yPoint.Z() - o[2]];
  const z = [zPoint.X() - o[0], zPoint.Y() - o[1], zPoint.Z() - o[2]];
  origin.delete();
  xPoint.delete();
  yPoint.delete();
  zPoint.delete();
  return [
    x[0],
    y[0],
    z[0],
    o[0],
    x[1],
    y[1],
    z[1],
    o[1],
    x[2],
    y[2],
    z[2],
    o[2]
  ];
};
var applyTransformation = (shape, transform) => {
  const manifoldTransform = manifoldTransformFromTransformation(transform);
  return shape.transform(manifoldTransform);
};

class MeshShape extends WrappingObj {
  constructor(manifoldShape) {
    super(manifoldShape);
    manifoldShape.numVert();
  }
  clone() {
    const mesh = this.wrapped.getMesh();
    const manifold = getManifold();
    const copy = new manifold.Manifold(mesh);
    return new MeshShape(copy);
  }
  fuse(other, _options) {
    const newShape = this.wrapped.add(other.wrapped);
    return new MeshShape(newShape);
  }
  cut(other, _options) {
    const newShape = this.wrapped.subtract(other.wrapped);
    return new MeshShape(newShape);
  }
  intersect(other) {
    const newShape = this.wrapped.intersect(other.wrapped);
    return new MeshShape(newShape);
  }
  translate(vectorOrxDist, yDist = 0, zDist = 0) {
    const translation = typeof vectorOrxDist === "number" ? [vectorOrxDist, yDist, zDist] : asVec3(vectorOrxDist);
    const newShape = this.wrapped.translate(translation);
    return new MeshShape(newShape);
  }
  translateX(distance) {
    return this.translate([distance, 0, 0]);
  }
  translateY(distance) {
    return this.translate([0, distance, 0]);
  }
  translateZ(distance) {
    return this.translate([0, 0, distance]);
  }
  rotate(angleOrVector, position = [0, 0, 0], direction = [0, 0, 1]) {
    if (typeof angleOrVector !== "number") {
      const newShape2 = this.wrapped.rotate(asVec3(angleOrVector));
      return new MeshShape(newShape2);
    }
    const transform = new Transformation;
    transform.rotate(angleOrVector, position, direction);
    const newShape = applyTransformation(this.wrapped, transform);
    return new MeshShape(newShape);
  }
  scale(scale2, center) {
    const transform = new Transformation;
    transform.scale(center ?? [0, 0, 0], scale2);
    const newShape = applyTransformation(this.wrapped, transform);
    return new MeshShape(newShape);
  }
  mirror(inputPlane, origin) {
    const transform = new Transformation;
    transform.mirror(inputPlane, origin);
    const newShape = applyTransformation(this.wrapped, transform);
    return new MeshShape(newShape);
  }
  simplify(tolerance) {
    const newShape = this.wrapped.simplify(tolerance);
    return new MeshShape(newShape);
  }
  refine(n) {
    return new MeshShape(this.wrapped.refine(n));
  }
  refineToLength(length) {
    return new MeshShape(this.wrapped.refineToLength(length));
  }
  refineToTolerance(tolerance) {
    return new MeshShape(this.wrapped.refineToTolerance(tolerance));
  }
  hull() {
    return new MeshShape(this.wrapped.hull());
  }
  asOriginal() {
    return new MeshShape(this.wrapped.asOriginal());
  }
  mesh() {
    const meshSource = this.wrapped.calculateNormals(0, 0.1);
    const mesh = meshSource.getMesh();
    const numProp = mesh.numProp ?? 3;
    const vertProperties = Array.from(mesh.vertProperties);
    const triangles = Array.from(mesh.triVerts);
    const vertices = [];
    const numVert = mesh.numVert ?? Math.floor(vertProperties.length / numProp);
    const normalOffset = numProp >= 6 ? 3 : -1;
    const normals = [];
    for (let i = 0;i < numVert; i++) {
      const base = i * numProp;
      vertices.push(vertProperties[base], vertProperties[base + 1], vertProperties[base + 2]);
      if (normalOffset >= 0 && normalOffset + 2 < numProp) {
        normals.push(vertProperties[base + normalOffset], vertProperties[base + normalOffset + 1], vertProperties[base + normalOffset + 2]);
      }
    }
    return {
      vertices,
      triangles,
      normals,
      vertProperties,
      numProp
    };
  }
  get boundingBox() {
    const bbox = this.wrapped.boundingBox();
    return BoundingBox$1.fromBounds([bbox.min[0], bbox.min[1], bbox.min[2]], [bbox.max[0], bbox.max[1], bbox.max[2]]);
  }
  volume() {
    return this.wrapped.volume();
  }
  surfaceArea() {
    return this.wrapped.surfaceArea();
  }
  numTri() {
    return this.wrapped.numTri();
  }
  numVert() {
    return this.wrapped.numVert();
  }
  numEdge() {
    return this.wrapped.numEdge();
  }
  get isEmpty() {
    return this.wrapped.isEmpty();
  }
  blobSTL({ binary = false } = {}) {
    const { vertices, triangles } = this.mesh();
    const numTriangles = triangles.length / 3;
    if (binary) {
      const bufferSize = 80 + 4 + numTriangles * 50;
      const buffer = new ArrayBuffer(bufferSize);
      const view = new DataView(buffer);
      view.setUint32(80, numTriangles, true);
      let offset2 = 84;
      for (let i = 0;i < numTriangles; i++) {
        const i0 = triangles[i * 3];
        const i1 = triangles[i * 3 + 1];
        const i2 = triangles[i * 3 + 2];
        const ax = vertices[i0 * 3], ay = vertices[i0 * 3 + 1], az = vertices[i0 * 3 + 2];
        const bx = vertices[i1 * 3], by = vertices[i1 * 3 + 1], bz = vertices[i1 * 3 + 2];
        const cx = vertices[i2 * 3], cy = vertices[i2 * 3 + 1], cz = vertices[i2 * 3 + 2];
        const ux = bx - ax, uy = by - ay, uz = bz - az;
        const vx = cx - ax, vy = cy - ay, vz = cz - az;
        let nx = uy * vz - uz * vy;
        let ny = uz * vx - ux * vz;
        let nz = ux * vy - uy * vx;
        const len = Math.sqrt(nx * nx + ny * ny + nz * nz);
        if (len > 0) {
          nx /= len;
          ny /= len;
          nz /= len;
        }
        view.setFloat32(offset2, nx, true);
        offset2 += 4;
        view.setFloat32(offset2, ny, true);
        offset2 += 4;
        view.setFloat32(offset2, nz, true);
        offset2 += 4;
        view.setFloat32(offset2, ax, true);
        offset2 += 4;
        view.setFloat32(offset2, ay, true);
        offset2 += 4;
        view.setFloat32(offset2, az, true);
        offset2 += 4;
        view.setFloat32(offset2, bx, true);
        offset2 += 4;
        view.setFloat32(offset2, by, true);
        offset2 += 4;
        view.setFloat32(offset2, bz, true);
        offset2 += 4;
        view.setFloat32(offset2, cx, true);
        offset2 += 4;
        view.setFloat32(offset2, cy, true);
        offset2 += 4;
        view.setFloat32(offset2, cz, true);
        offset2 += 4;
        view.setUint16(offset2, 0, true);
        offset2 += 2;
      }
      return new Blob([buffer], { type: "application/sla" });
    }
    const lines = ["solid mesh"];
    for (let i = 0;i < numTriangles; i++) {
      const i0 = triangles[i * 3];
      const i1 = triangles[i * 3 + 1];
      const i2 = triangles[i * 3 + 2];
      const ax = vertices[i0 * 3], ay = vertices[i0 * 3 + 1], az = vertices[i0 * 3 + 2];
      const bx = vertices[i1 * 3], by = vertices[i1 * 3 + 1], bz = vertices[i1 * 3 + 2];
      const cx = vertices[i2 * 3], cy = vertices[i2 * 3 + 1], cz = vertices[i2 * 3 + 2];
      const ux = bx - ax, uy = by - ay, uz = bz - az;
      const vx = cx - ax, vy = cy - ay, vz = cz - az;
      let nx = uy * vz - uz * vy;
      let ny = uz * vx - ux * vz;
      let nz = ux * vy - uy * vx;
      const len = Math.sqrt(nx * nx + ny * ny + nz * nz);
      if (len > 0) {
        nx /= len;
        ny /= len;
        nz /= len;
      }
      lines.push(`facet normal ${nx} ${ny} ${nz}`);
      lines.push("  outer loop");
      lines.push(`    vertex ${ax} ${ay} ${az}`);
      lines.push(`    vertex ${bx} ${by} ${bz}`);
      lines.push(`    vertex ${cx} ${cy} ${cz}`);
      lines.push("  endloop");
      lines.push("endfacet");
    }
    lines.push("endsolid mesh");
    return new Blob([lines.join(`
`)], { type: "application/sla" });
  }
}
var DIRECTIONS = {
  X: [1, 0, 0],
  Y: [0, 1, 0],
  Z: [0, 0, 1]
};
var PLANE_TO_DIR = {
  YZ: [1, 0, 0],
  XZ: [0, 1, 0],
  XY: [0, 0, 1]
};

class Finder {
  constructor() {
    this.filters = [];
  }
  delete() {
    this.filters = [];
  }
  and(findersList) {
    findersList.forEach((f) => f(this));
    return this;
  }
  not(finderFun) {
    const finder = new this.constructor;
    finderFun(finder);
    const notFilter = ({ element }) => !finder.shouldKeep(element);
    this.filters.push(notFilter);
    return this;
  }
  either(findersList) {
    const builtFinders = findersList.map((finderFunction) => {
      const finder = new this.constructor;
      finderFunction(finder);
      return finder;
    });
    const eitherFilter = ({ element }) => builtFinders.some((finder) => finder.shouldKeep(element));
    this.filters.push(eitherFilter);
    return this;
  }
  find(shape, { unique = false } = {}) {
    const elements = this.applyFilter(shape);
    if (unique) {
      if (elements.length !== 1) {
        console.error(elements);
        throw new Error("Finder has not found a unique solution");
      }
      return elements[0];
    }
    return elements;
  }
}

class ProgressRange extends WrappingObj {
  constructor() {
    const oc = getOC();
    super(new oc.Message_ProgressRange_1);
  }
}

class PhysicalProperties extends WrappingObj {
  get centerOfMass() {
    const r = GCWithScope();
    const pnt2 = r(this.wrapped.CentreOfMass());
    return [pnt2.X(), pnt2.Y(), pnt2.Z()];
  }
}

class VolumePhysicalProperties extends PhysicalProperties {
  get volume() {
    return this.wrapped.Mass();
  }
}

class SurfacePhysicalProperties extends PhysicalProperties {
  get area() {
    return this.wrapped.Mass();
  }
}
function measureShapeSurfaceProperties(shape) {
  const oc = getOC();
  const properties = new oc.GProp_GProps_1;
  oc.BRepGProp.SurfaceProperties_1(shape.wrapped, properties, false, false);
  return new SurfacePhysicalProperties(properties);
}
function measureShapeVolumeProperties(shape) {
  const oc = getOC();
  const properties = new oc.GProp_GProps_1;
  oc.BRepGProp.VolumeProperties_1(shape.wrapped, properties, false, false, false);
  return new VolumePhysicalProperties(properties);
}
function measureVolume(shape) {
  return measureShapeVolumeProperties(shape).volume;
}
function measureArea(shape) {
  return measureShapeSurfaceProperties(shape).area;
}
class DistanceQuery extends WrappingObj {
  constructor(shape) {
    const oc = getOC();
    super(new oc.BRepExtrema_DistShapeShape_1);
    this.wrapped.LoadS1(shape.wrapped);
  }
  distanceTo(shape) {
    this.wrapped.LoadS2(shape.wrapped);
    const progress = new ProgressRange;
    this.wrapped.Perform(progress.wrapped);
    return this.wrapped.Value();
  }
}

class Finder3d extends Finder {
  when(filter) {
    this.filters.push(filter);
    return this;
  }
  inList(elementList) {
    const elementInList = ({ element }) => {
      return !!elementList.find((e) => e.isSame(element));
    };
    this.filters.push(elementInList);
    return this;
  }
  atAngleWith(direction = "Z", angle = 0) {
    let myDirection;
    if (typeof direction === "string") {
      myDirection = new Vector(DIRECTIONS[direction]);
    } else {
      myDirection = new Vector(direction);
    }
    const checkAngle = ({ normal }) => {
      if (!normal)
        return false;
      const angleOfNormal = Math.acos(Math.abs(normal.dot(myDirection)));
      return Math.abs(angleOfNormal - DEG2RAD * angle) < 0.000001;
    };
    this.filters.push(checkAngle);
    return this;
  }
  atDistance(distance, point = [0, 0, 0]) {
    const vertex = makeVertex(point);
    const query = new DistanceQuery(vertex);
    const checkPoint = ({ element }) => {
      return Math.abs(query.distanceTo(element) - distance) < 0.000001;
    };
    this.filters.push(checkPoint);
    return this;
  }
  containsPoint(point) {
    return this.atDistance(0, point);
  }
  withinDistance(distance, point = [0, 0, 0]) {
    const vertex = makeVertex(point);
    const query = new DistanceQuery(vertex);
    const checkPoint = ({ element }) => {
      return query.distanceTo(element) - distance < 0.000001;
    };
    this.filters.push(checkPoint);
    return this;
  }
  inBox(corner1, corner2) {
    const box = makeBox(corner1, corner2);
    return this.inShape(box);
  }
  inShape(shape) {
    const query = new DistanceQuery(shape);
    const checkPoint = ({ element }) => {
      return query.distanceTo(element) < 0.000001;
    };
    this.filters.push(checkPoint);
    return this;
  }
}

class EdgeFinder extends Finder3d {
  clone() {
    const ef = new EdgeFinder;
    ef.filters = [...this.filters];
    return ef;
  }
  inDirection(direction) {
    return this.atAngleWith(direction, 0);
  }
  ofLength(length) {
    const check2 = ({ element }) => {
      if (typeof length === "number")
        return Math.abs(element.length - length) < 0.000000001;
      return length(element.length);
    };
    this.filters.push(check2);
    return this;
  }
  ofCurveType(curveType) {
    const check2 = ({ element }) => {
      return element.geomType === curveType;
    };
    this.filters.push(check2);
    return this;
  }
  parallelTo(plane) {
    if (typeof plane === "string" && PLANE_TO_DIR[plane])
      return this.atAngleWith(PLANE_TO_DIR[plane], 90);
    if (typeof plane !== "string" && plane instanceof Plane)
      return this.atAngleWith(plane.zDir, 90);
    if (typeof plane !== "string" && plane.normalAt) {
      const normal = plane.normalAt();
      this.atAngleWith(normal, 90);
      return this;
    }
    return this;
  }
  inPlane(inputPlane, origin) {
    const plane = inputPlane instanceof Plane ? makePlane(inputPlane) : makePlane(inputPlane, origin);
    this.parallelTo(plane);
    const firstPointInPlane = ({ element }) => {
      const point = element.startPoint;
      const projectedPoint = point.projectToPlane(plane);
      const isSamePoint = point.equals(projectedPoint);
      return isSamePoint;
    };
    this.filters.push(firstPointInPlane);
    return this;
  }
  shouldKeep(element) {
    let normal = null;
    try {
      normal = element.tangentAt().normalized();
    } catch (error) {
      console.error("failed to compute a normal", error);
    }
    return this.filters.every((filter) => filter({ normal, element }));
  }
  applyFilter(shape) {
    return shape.edges.filter((edge) => {
      const shouldKeep = this.shouldKeep(edge);
      return shouldKeep;
    });
  }
}

class FaceFinder extends Finder3d {
  clone() {
    const ff = new FaceFinder;
    ff.filters = [...this.filters];
    return ff;
  }
  parallelTo(plane) {
    if (typeof plane === "string" && PLANE_TO_DIR[plane])
      return this.atAngleWith(PLANE_TO_DIR[plane]);
    if (typeof plane !== "string" && plane instanceof Plane)
      return this.atAngleWith(plane.zDir);
    if (typeof plane !== "string" && plane.normalAt) {
      const normal = plane.normalAt();
      this.atAngleWith(normal);
      return this;
    }
    return this;
  }
  ofSurfaceType(surfaceType) {
    const check2 = ({ element }) => {
      return element.geomType === surfaceType;
    };
    this.filters.push(check2);
    return this;
  }
  inPlane(inputPlane, origin) {
    const plane = inputPlane instanceof Plane ? makePlane(inputPlane) : makePlane(inputPlane, origin);
    this.parallelTo(plane);
    const centerInPlane = ({ element }) => {
      const point = element.center;
      const projectedPoint = point.projectToPlane(plane);
      const isSamePoint = point.equals(projectedPoint);
      return isSamePoint;
    };
    this.filters.push(centerInPlane);
    return this;
  }
  shouldKeep(element) {
    const normal = element.normalAt();
    const shouldKeep = this.filters.every((filter) => filter({ normal, element }));
    return shouldKeep;
  }
  applyFilter(shape) {
    return shape.faces.filter((face) => {
      const shouldKeep = this.shouldKeep(face);
      return shouldKeep;
    });
  }
}
var blueprintCorners = function(blueprint) {
  return blueprint.curves.map((curve, index) => {
    return {
      firstCurve: curve,
      secondCurve: blueprint.curves[(index + 1) % blueprint.curves.length],
      point: curve.lastPoint
    };
  });
};
var PI_2 = 2 * Math.PI;
var positiveHalfAngle = (angle) => {
  const limitedAngle = angle % PI_2;
  const coterminalAngle = limitedAngle < 0 ? limitedAngle + PI_2 : limitedAngle;
  if (coterminalAngle < Math.PI)
    return coterminalAngle;
  if (coterminalAngle === Math.PI)
    return 0;
  return Math.abs(coterminalAngle - PI_2);
};

class CornerFinder extends Finder {
  clone() {
    const ef = new CornerFinder;
    ef.filters = [...this.filters];
    return ef;
  }
  inList(elementList) {
    const elementInList = ({ element }) => {
      return !!elementList.find((e) => samePoint$2(e, element.point));
    };
    this.filters.push(elementInList);
    return this;
  }
  atDistance(distance, point = [0, 0]) {
    function elementAtDistance({ element }) {
      return Math.abs(distance2d(point, element.point) - distance) < 0.000000001;
    }
    this.filters.push(elementAtDistance);
    return this;
  }
  atPoint(point) {
    function elementAtPoint({ element }) {
      return samePoint$2(point, element.point);
    }
    this.filters.push(elementAtPoint);
    return this;
  }
  inBox(corner1, corner2) {
    const [x1, y1] = corner1;
    const [x2, y2] = corner2;
    const minX = Math.min(x1, x2);
    const maxX = Math.max(x1, x2);
    const minY = Math.min(y1, y2);
    const maxY = Math.max(y1, y2);
    function elementInBox({ element }) {
      const [x, y] = element.point;
      return x >= minX && x <= maxX && y >= minY && y <= maxY;
    }
    this.filters.push(elementInBox);
    return this;
  }
  ofAngle(angle) {
    function elementOfAngle({ element }) {
      const tgt1 = element.firstCurve.tangentAt(1);
      const tgt2 = element.secondCurve.tangentAt(0);
      return Math.abs(positiveHalfAngle(angle2d(tgt1, tgt2)) - positiveHalfAngle(DEG2RAD * angle)) < 0.000000001;
    }
    this.filters.push(elementOfAngle);
    return this;
  }
  shouldKeep(element) {
    const shouldKeep = this.filters.every((filter) => filter({ normal: null, element }));
    return shouldKeep;
  }
  applyFilter(blueprint) {
    return blueprintCorners(blueprint).filter((corner) => {
      const shouldKeep = this.shouldKeep(corner);
      return shouldKeep;
    });
  }
}
function getSingleFace(f, shape) {
  if (f instanceof Face)
    return f;
  const finder = f instanceof FaceFinder ? f : f(new FaceFinder);
  return finder.find(shape, { unique: true });
}
function isNumber(r) {
  return typeof r === "number";
}
function isChamferRadius(r) {
  if (typeof r === "number")
    return true;
  if (typeof r === "object" && r !== null) {
    const obj = r;
    return "distances" in obj && Array.isArray(obj.distances) && "selectedFace" in obj || "distance" in obj && "angle" in obj && "selectedFace" in obj;
  }
  return false;
}
function isFilletRadius(r) {
  if (typeof r === "number")
    return true;
  if (Array.isArray(r) && r.length === 2) {
    return r.every(isNumber);
  }
  return false;
}
var asTopo = (entity) => {
  const oc = getOC();
  return {
    vertex: oc.TopAbs_ShapeEnum.TopAbs_VERTEX,
    wire: oc.TopAbs_ShapeEnum.TopAbs_WIRE,
    face: oc.TopAbs_ShapeEnum.TopAbs_FACE,
    shell: oc.TopAbs_ShapeEnum.TopAbs_SHELL,
    solid: oc.TopAbs_ShapeEnum.TopAbs_SOLID,
    solidCompound: oc.TopAbs_ShapeEnum.TopAbs_COMPSOLID,
    compound: oc.TopAbs_ShapeEnum.TopAbs_COMPOUND,
    edge: oc.TopAbs_ShapeEnum.TopAbs_EDGE,
    shape: oc.TopAbs_ShapeEnum.TopAbs_SHAPE
  }[entity];
};
var iterTopo = function* iterTopo2(shape, topo) {
  const oc = getOC();
  const explorer = new oc.TopExp_Explorer_2(shape, asTopo(topo), asTopo("shape"));
  const hashes = /* @__PURE__ */ new Map;
  while (explorer.More()) {
    const item = explorer.Current();
    const hash = item.HashCode(HASH_CODE_MAX);
    if (!hashes.get(hash)) {
      hashes.set(hash, true);
      yield item;
    }
    explorer.Next();
  }
  explorer.delete();
};
var shapeType = (shape) => {
  if (shape.IsNull())
    throw new Error("This shape has not type, it is null");
  return shape.ShapeType();
};
class Shape extends WrappingObj {
  constructor(ocShape) {
    super(ocShape);
  }
  clone() {
    return new this.constructor(downcast(this.wrapped));
  }
  serialize() {
    const oc = getOC();
    return oc.BRepToolsWrapper.Write(this.wrapped);
  }
  get hashCode() {
    return this.wrapped.HashCode(HASH_CODE_MAX);
  }
  get isNull() {
    return this.wrapped.IsNull();
  }
  isSame(other) {
    return this.wrapped.IsSame(other.wrapped);
  }
  isEqual(other) {
    return this.wrapped.IsEqual(other.wrapped);
  }
  asShape3D() {
    if (isShape3D(this)) {
      return this;
    }
    throw new Error("Shape is not a 3D shape");
  }
  simplify() {
    const oc = getOC();
    const shapeUpgrader = new oc.ShapeUpgrade_UnifySameDomain_2(this.wrapped, true, true, false);
    shapeUpgrader.Build();
    const newShape = cast(shapeUpgrader.Shape());
    shapeUpgrader.delete();
    if (this.constructor !== newShape.constructor)
      throw new Error("unexpected types mismatch");
    return newShape;
  }
  translate(vectorOrxDist, yDist = 0, zDist = 0) {
    const translation = typeof vectorOrxDist === "number" ? [vectorOrxDist, yDist, zDist] : vectorOrxDist;
    const newShape = cast(translate(this.wrapped, translation));
    this.delete();
    if (this.constructor !== newShape.constructor)
      throw new Error("unexpected types mismatch");
    return newShape;
  }
  translateX(distance) {
    return this.translate([distance, 0, 0]);
  }
  translateY(distance) {
    return this.translate([0, distance, 0]);
  }
  translateZ(distance) {
    return this.translate([0, 0, distance]);
  }
  rotate(angle, position = [0, 0, 0], direction = [0, 0, 1]) {
    const newShape = cast(rotate(this.wrapped, angle, position, direction));
    this.delete();
    if (this.constructor !== newShape.constructor)
      throw new Error("unexpected types mismatch");
    return newShape;
  }
  mirror(inputPlane, origin) {
    const newShape = cast(mirror(this.wrapped, inputPlane, origin));
    this.delete();
    if (this.constructor !== newShape.constructor)
      throw new Error("unexpected types mismatch");
    return newShape;
  }
  scale(scale$1, center = [0, 0, 0]) {
    const newShape = cast(scale(this.wrapped, center, scale$1));
    this.delete();
    if (this.constructor !== newShape.constructor)
      throw new Error("unexpected types mismatch");
    return newShape;
  }
  _iterTopo(topo) {
    return iterTopo(this.wrapped, topo);
  }
  _listTopo(topo) {
    return Array.from(this._iterTopo(topo)).map((e) => {
      return downcast(e);
    });
  }
  get edges() {
    return this._listTopo("edge").map((e) => new Edge(e));
  }
  get faces() {
    return this._listTopo("face").map((e) => new Face(e));
  }
  get wires() {
    return this._listTopo("wire").map((e) => new Wire(e));
  }
  get boundingBox() {
    const bbox = new BoundingBox$1;
    this.oc.BRepBndLib.Add(this.wrapped, bbox.wrapped, true);
    return bbox;
  }
  _mesh({ tolerance = 0.001, angularTolerance = 0.1 } = {}) {
    new this.oc.BRepMesh_IncrementalMesh_2(this.wrapped, tolerance, false, angularTolerance, false);
  }
  mesh({ tolerance = 0.001, angularTolerance = 0.1 } = {}) {
    this._mesh({ tolerance, angularTolerance });
    let triangles = [];
    let vertices = [];
    let normals = [];
    const faceGroups = [];
    for (const face of this.faces) {
      const tri = face.triangulation(vertices.length / 3);
      if (!tri)
        continue;
      const { trianglesIndexes, vertices: faceVertices, verticesNormals } = tri;
      faceGroups.push({
        start: triangles.length,
        count: trianglesIndexes.length,
        faceId: face.hashCode
      });
      triangles = triangles.concat(trianglesIndexes);
      vertices = vertices.concat(faceVertices);
      normals = normals.concat(verticesNormals);
    }
    return {
      triangles,
      vertices,
      normals,
      faceGroups
    };
  }
  meshEdges({ tolerance = 0.001, angularTolerance = 0.1 } = {}) {
    var _a;
    const r = GCWithScope();
    const recordedEdges = /* @__PURE__ */ new Set;
    const lines = [];
    const edgeGroups = [];
    const addEdge = () => {
      const start = lines.length;
      let previousPoint = null;
      return [
        (p) => {
          if (previousPoint) {
            lines.push(...previousPoint);
            previousPoint = [p.X(), p.Y(), p.Z()];
            lines.push(...previousPoint);
          } else {
            previousPoint = [p.X(), p.Y(), p.Z()];
          }
        },
        (edgeHash) => {
          edgeGroups.push({
            start: start / 3,
            count: (lines.length - start) / 3,
            edgeId: edgeHash
          });
          recordedEdges.add(edgeHash);
        }
      ];
    };
    const aLocation = r(new this.oc.TopLoc_Location_1);
    for (const face of this.faces) {
      const triangulation = r(this.oc.BRep_Tool.Triangulation(face.wrapped, aLocation, 0));
      if (triangulation.IsNull()) {
        continue;
      }
      const tri = triangulation.get();
      for (const edge of face.edges) {
        r(edge);
        if (recordedEdges.has(edge.hashCode))
          continue;
        const edgeLoc = r(new this.oc.TopLoc_Location_1);
        const polygon = r(this.oc.BRep_Tool.PolygonOnTriangulation_1(edge.wrapped, triangulation, edgeLoc));
        const edgeNodes = (_a = polygon == null ? undefined : polygon.get()) == null ? undefined : _a.Nodes();
        if (!edgeNodes) {
          continue;
        }
        r(edgeNodes);
        const [recordPoint, done] = addEdge();
        for (let i = edgeNodes.Lower();i <= edgeNodes.Upper(); i++) {
          const p = r(r(tri.Node(edgeNodes.Value(i))).Transformed(edgeLoc.Transformation()));
          recordPoint(p);
        }
        done(edge.hashCode);
      }
    }
    for (const edge of this.edges) {
      r(edge);
      const edgeHash = edge.hashCode;
      if (recordedEdges.has(edgeHash))
        continue;
      const adaptorCurve = r(new this.oc.BRepAdaptor_Curve_2(edge.wrapped));
      const tangDef = r(new this.oc.GCPnts_TangentialDeflection_2(adaptorCurve, tolerance, angularTolerance, 2, 0.000000001, 0.0000001));
      const [recordPoint, done] = addEdge();
      for (let j = 0;j < tangDef.NbPoints(); j++) {
        const p = r(tangDef.Value(j + 1).Transformed(aLocation.Transformation()));
        recordPoint(p);
      }
      done(edgeHash);
    }
    return { lines, edgeGroups };
  }
  blobSTEP() {
    const filename = "blob.step";
    const writer = new this.oc.STEPControl_Writer_1;
    this.oc.Interface_Static.SetIVal("write.step.schema", 5);
    writer.Model(true).delete();
    const progress = new this.oc.Message_ProgressRange_1;
    writer.Transfer(this.wrapped, this.oc.STEPControl_StepModelType.STEPControl_AsIs, true, progress);
    const done = writer.Write(filename);
    writer.delete();
    progress.delete();
    if (done === this.oc.IFSelect_ReturnStatus.IFSelect_RetDone) {
      const file = this.oc.FS.readFile("/" + filename);
      this.oc.FS.unlink("/" + filename);
      const blob = new Blob([file], { type: "application/STEP" });
      return blob;
    } else {
      throw new Error("WRITE STEP FILE FAILED.");
    }
  }
  blobSTL({
    tolerance = 0.001,
    angularTolerance = 0.1,
    binary = false
  } = {}) {
    this._mesh({ tolerance, angularTolerance });
    const filename = "blob.stl";
    const done = this.oc.StlAPI.Write(this.wrapped, filename, !binary);
    if (done) {
      const file = this.oc.FS.readFile("/" + filename);
      this.oc.FS.unlink("/" + filename);
      const blob = new Blob([file], { type: "application/sla" });
      return blob;
    } else {
      throw new Error("WRITE STL FILE FAILED.");
    }
  }
}

class Vertex extends Shape {
  asTuple() {
    const pnt2 = this.oc.BRep_Tool.Pnt(this.wrapped);
    const tuple = [pnt2.X(), pnt2.Y(), pnt2.Z()];
    pnt2.delete();
    return tuple;
  }
}

class _1DShape extends Shape {
  get repr() {
    const { startPoint, endPoint } = this;
    const retVal = `start: (${this.startPoint.repr}) end:(${this.endPoint.repr})`;
    startPoint.delete();
    endPoint.delete();
    return retVal;
  }
  get curve() {
    return new Curve(this._geomAdaptor());
  }
  get startPoint() {
    return this.curve.startPoint;
  }
  get endPoint() {
    return this.curve.endPoint;
  }
  tangentAt(position = 0) {
    return this.curve.tangentAt(position);
  }
  pointAt(position = 0) {
    return this.curve.pointAt(position);
  }
  get isClosed() {
    return this.curve.isClosed;
  }
  get isPeriodic() {
    return this.curve.isPeriodic;
  }
  get period() {
    return this.curve.period;
  }
  get geomType() {
    return this.curve.curveType;
  }
  get length() {
    const properties = new this.oc.GProp_GProps_1;
    this.oc.BRepGProp.LinearProperties(this.wrapped, properties, true, false);
    const length = properties.Mass();
    properties.delete();
    return length;
  }
  get orientation() {
    const orient = this.wrapped.Orientation_1();
    if (orient === this.oc.TopAbs_Orientation.TopAbs_FORWARD)
      return "forward";
    return "backward";
  }
  flipOrientation() {
    const flipped = this.wrapped.Reversed();
    return cast(flipped);
  }
}

class Curve extends WrappingObj {
  get repr() {
    const { startPoint, endPoint } = this;
    const retVal = `start: (${this.startPoint.repr}) end:(${this.endPoint.repr}}`;
    startPoint.delete();
    endPoint.delete();
    return retVal;
  }
  get curveType() {
    const technicalType = this.wrapped.GetType && this.wrapped.GetType();
    return findCurveType(technicalType);
  }
  get startPoint() {
    const umin = this.wrapped.Value(this.wrapped.FirstParameter());
    return new Vector(umin);
  }
  get endPoint() {
    const umax = this.wrapped.Value(this.wrapped.LastParameter());
    return new Vector(umax);
  }
  _mapParameter(position) {
    const firstParam = this.wrapped.FirstParameter();
    const lastParam = this.wrapped.LastParameter();
    return firstParam + (lastParam - firstParam) * position;
  }
  pointAt(position = 0.5) {
    return new Vector(this.wrapped.Value(this._mapParameter(position)));
  }
  tangentAt(position = 0.5) {
    const pos = this._mapParameter(position);
    const tmp = new this.oc.gp_Pnt_1;
    const res = new this.oc.gp_Vec_1;
    this.wrapped.D1(pos, tmp, res);
    const tangent = new Vector(res);
    tmp.delete();
    res.delete();
    return tangent;
  }
  get isClosed() {
    const isClosed = this.wrapped.IsClosed();
    return isClosed;
  }
  get isPeriodic() {
    const isPeriodic = this.wrapped.IsPeriodic();
    return isPeriodic;
  }
  get period() {
    const period = this.wrapped.Period();
    return period;
  }
}

class Edge extends _1DShape {
  _geomAdaptor() {
    return new this.oc.BRepAdaptor_Curve_2(this.wrapped);
  }
}

class Wire extends _1DShape {
  _geomAdaptor() {
    return new this.oc.BRepAdaptor_CompCurve_2(this.wrapped, false);
  }
  offset2D(offset2, kind = "arc") {
    const kinds = {
      arc: this.oc.GeomAbs_JoinType.GeomAbs_Arc,
      intersection: this.oc.GeomAbs_JoinType.GeomAbs_Intersection,
      tangent: this.oc.GeomAbs_JoinType.GeomAbs_Tangent
    };
    const offsetter = new this.oc.BRepOffsetAPI_MakeOffset_3(this.wrapped, kinds[kind], false);
    offsetter.Perform(offset2, 0);
    const newShape = cast(offsetter.Shape());
    offsetter.delete();
    this.delete();
    if (!(newShape instanceof Wire))
      throw new Error("Could not offset with a wire");
    return newShape;
  }
}

class Surface extends WrappingObj {
  get surfaceType() {
    const ga = this.oc.GeomAbs_SurfaceType;
    const CAST_MAP = /* @__PURE__ */ new Map([
      [ga.GeomAbs_Plane, "PLANE"],
      [ga.GeomAbs_Cylinder, "CYLINDRE"],
      [ga.GeomAbs_Cone, "CONE"],
      [ga.GeomAbs_Sphere, "SPHERE"],
      [ga.GeomAbs_Torus, "TORUS"],
      [ga.GeomAbs_BezierSurface, "BEZIER_SURFACE"],
      [ga.GeomAbs_BSplineSurface, "BSPLINE_SURFACE"],
      [ga.GeomAbs_SurfaceOfRevolution, "REVOLUTION_SURFACE"],
      [ga.GeomAbs_SurfaceOfExtrusion, "EXTRUSION_SURFACE"],
      [ga.GeomAbs_OffsetSurface, "OFFSET_SURFACE"],
      [ga.GeomAbs_OtherSurface, "OTHER_SURFACE"]
    ]);
    const shapeType2 = CAST_MAP.get(this.wrapped.GetType());
    if (!shapeType2)
      throw new Error("surface type not found");
    return shapeType2;
  }
}

class Face extends Shape {
  _geomAdaptor() {
    return new this.oc.BRepAdaptor_Surface_2(this.wrapped, false);
  }
  get surface() {
    return new Surface(this._geomAdaptor());
  }
  get orientation() {
    const orient = this.wrapped.Orientation_1();
    if (orient === this.oc.TopAbs_Orientation.TopAbs_FORWARD)
      return "forward";
    return "backward";
  }
  flipOrientation() {
    const flipped = this.wrapped.Reversed();
    return cast(flipped);
  }
  get geomType() {
    const surface = this.surface;
    const geomType = surface.surfaceType;
    surface.delete();
    return geomType;
  }
  get UVBounds() {
    const uMin = { current: 0 };
    const uMax = { current: 0 };
    const vMin = { current: 0 };
    const vMax = { current: 0 };
    this.oc.BRepTools.UVBounds_1(this.wrapped, uMin, uMax, vMin, vMax);
    return {
      uMin: uMin.current,
      uMax: uMax.current,
      vMin: vMin.current,
      vMax: vMax.current
    };
  }
  pointOnSurface(u, v) {
    const { uMin, uMax, vMin, vMax } = this.UVBounds;
    const surface = this._geomAdaptor();
    const p = new this.oc.gp_Pnt_1;
    const absoluteU = u * (uMax - uMin) + uMin;
    const absoluteV = v * (vMax - vMin) + vMin;
    surface.D0(absoluteU, absoluteV, p);
    const point = new Vector(p);
    surface.delete();
    p.delete();
    return point;
  }
  uvCoordinates(point) {
    const r = GCWithScope();
    const surface = r(this.oc.BRep_Tool.Surface_2(this.wrapped));
    const projectedPoint = r(new this.oc.GeomAPI_ProjectPointOnSurf_2(r(asPnt(point)), surface, this.oc.Extrema_ExtAlgo.Extrema_ExtAlgo_Grad));
    const uPtr = { current: 0 };
    const vPtr = { current: 0 };
    projectedPoint.LowerDistanceParameters(uPtr, vPtr);
    return [uPtr.current, vPtr.current];
  }
  normalAt(locationVector) {
    let u = 0;
    let v = 0;
    const r = GCWithScope();
    if (!locationVector) {
      const { uMin, uMax, vMin, vMax } = this.UVBounds;
      u = 0.5 * (uMin + uMax);
      v = 0.5 * (vMin + vMax);
    } else {
      [u, v] = this.uvCoordinates(locationVector);
    }
    const p = r(new this.oc.gp_Pnt_1);
    const vn = r(new this.oc.gp_Vec_1);
    const props = r(new this.oc.BRepGProp_Face_2(this.wrapped, false));
    props.Normal(u, v, p, vn);
    const normal = new Vector(vn);
    return normal;
  }
  get center() {
    const properties = new this.oc.GProp_GProps_1;
    this.oc.BRepGProp.SurfaceProperties_2(this.wrapped, properties, 0.0000001, true);
    const center = new Vector(properties.CentreOfMass());
    properties.delete();
    return center;
  }
  outerWire() {
    const newVal = new Wire(this.oc.BRepTools.OuterWire(this.wrapped));
    this.delete();
    return newVal;
  }
  innerWires() {
    const outer = this.clone().outerWire();
    const innerWires = this.wires.filter((w) => !outer.isSame(w));
    outer.delete();
    this.delete();
    return innerWires;
  }
  triangulation(index0 = 0) {
    const r = GCWithScope();
    const aLocation = r(new this.oc.TopLoc_Location_1);
    const triangulation = r(this.oc.BRep_Tool.Triangulation(this.wrapped, aLocation, 0));
    if (triangulation.IsNull())
      return null;
    const transformation = r(aLocation.Transformation());
    const triangulatedFace = {
      vertices: [],
      trianglesIndexes: [],
      verticesNormals: []
    };
    const tri = triangulation.get();
    const nbNodes = tri.NbNodes();
    triangulatedFace.vertices = new Array(nbNodes * 3);
    for (let i = 1;i <= nbNodes; i++) {
      const p = r(r(tri.Node(i)).Transformed(transformation));
      triangulatedFace.vertices[(i - 1) * 3 + 0] = p.X();
      triangulatedFace.vertices[(i - 1) * 3 + 1] = p.Y();
      triangulatedFace.vertices[(i - 1) * 3 + 2] = p.Z();
    }
    const normalsArray = r(new this.oc.TColgp_Array1OfDir_2(1, nbNodes));
    const pc = r(new this.oc.Poly_Connect_2(triangulation));
    this.oc.StdPrs_ToolTriangulatedShape.Normal(this.wrapped, pc, normalsArray);
    triangulatedFace.verticesNormals = new Array(normalsArray.Length() * 3);
    for (let i = normalsArray.Lower();i <= normalsArray.Upper(); i++) {
      const d = r(r(normalsArray.Value(i)).Transformed(transformation));
      triangulatedFace.verticesNormals[(i - 1) * 3 + 0] = d.X();
      triangulatedFace.verticesNormals[(i - 1) * 3 + 1] = d.Y();
      triangulatedFace.verticesNormals[(i - 1) * 3 + 2] = d.Z();
    }
    const orient = this.orientation;
    const nbTriangles = tri.NbTriangles();
    triangulatedFace.trianglesIndexes = new Array(nbTriangles * 3);
    let validFaceTriCount = 0;
    for (let nt = 1;nt <= nbTriangles; nt++) {
      const t = r(tri.Triangle(nt));
      let n1 = t.Value(1);
      let n2 = t.Value(2);
      const n3 = t.Value(3);
      if (orient === "backward") {
        const tmp = n1;
        n1 = n2;
        n2 = tmp;
      }
      triangulatedFace.trianglesIndexes[validFaceTriCount * 3 + 0] = n1 - 1 + index0;
      triangulatedFace.trianglesIndexes[validFaceTriCount * 3 + 1] = n2 - 1 + index0;
      triangulatedFace.trianglesIndexes[validFaceTriCount * 3 + 2] = n3 - 1 + index0;
      validFaceTriCount++;
    }
    return triangulatedFace;
  }
}

class _3DShape extends Shape {
  fuse(other, {
    optimisation = "none"
  } = {}) {
    const r = GCWithScope();
    const progress = r(new this.oc.Message_ProgressRange_1);
    const newBody = r(new this.oc.BRepAlgoAPI_Fuse_3(this.wrapped, other.wrapped, progress));
    if (optimisation === "commonFace") {
      newBody.SetGlue(this.oc.BOPAlgo_GlueEnum.BOPAlgo_GlueShift);
    }
    if (optimisation === "sameFace") {
      newBody.SetGlue(this.oc.BOPAlgo_GlueEnum.BOPAlgo_GlueFull);
    }
    newBody.Build(progress);
    newBody.SimplifyResult(true, true, 0.001);
    const newShape = cast(newBody.Shape());
    if (!isShape3D(newShape))
      throw new Error("Could not fuse as a 3d shape");
    return newShape;
  }
  cut(tool, {
    optimisation = "none"
  } = {}) {
    const r = GCWithScope();
    const progress = r(new this.oc.Message_ProgressRange_1);
    const cutter = r(new this.oc.BRepAlgoAPI_Cut_3(this.wrapped, tool.wrapped, progress));
    if (optimisation === "commonFace") {
      cutter.SetGlue(this.oc.BOPAlgo_GlueEnum.BOPAlgo_GlueShift);
    }
    if (optimisation === "sameFace") {
      cutter.SetGlue(this.oc.BOPAlgo_GlueEnum.BOPAlgo_GlueFull);
    }
    cutter.Build(progress);
    cutter.SimplifyResult(true, true, 0.001);
    const newShape = cast(cutter.Shape());
    if (!isShape3D(newShape))
      throw new Error("Could not cut as a 3d shape");
    return newShape;
  }
  intersect(tool) {
    const r = GCWithScope();
    const progress = r(new this.oc.Message_ProgressRange_1);
    const intersector = r(new this.oc.BRepAlgoAPI_Common_3(this.wrapped, tool.wrapped, progress));
    intersector.Build(progress);
    intersector.SimplifyResult(true, true, 0.001);
    const newShape = cast(intersector.Shape());
    if (!isShape3D(newShape))
      throw new Error("Could not intersect as a 3d shape");
    return newShape;
  }
  meshShape(options) {
    const { triangles, vertices } = this.mesh(options);
    const tol = (options == null ? undefined : options.tolerance) ?? 0.000001;
    const scale2 = tol === 0 ? 0 : 1 / tol;
    const keyFor = (x, y, z) => {
      if (scale2 === 0)
        return `${x}|${y}|${z}`;
      return `${Math.round(x * scale2)}|${Math.round(y * scale2)}|${Math.round(z * scale2)}`;
    };
    const mergeFrom = [];
    const mergeTo = [];
    const seen = /* @__PURE__ */ new Map;
    for (let i = 0;i < vertices.length; i += 3) {
      const x = vertices[i];
      const y = vertices[i + 1];
      const z = vertices[i + 2];
      const key = keyFor(x, y, z);
      const existing = seen.get(key);
      const idx = i / 3;
      if (existing !== undefined) {
        mergeFrom.push(idx);
        mergeTo.push(existing);
      } else {
        seen.set(key, idx);
      }
    }
    const numProp = 3;
    const vertProperties = new Float32Array(vertices);
    const meshData = {
      vertProperties,
      triVerts: new Uint32Array(triangles),
      numProp,
      mergeFromVert: mergeFrom.length ? new Uint32Array(mergeFrom) : undefined,
      mergeToVert: mergeTo.length ? new Uint32Array(mergeTo) : undefined
    };
    const manifold = getManifold();
    const mesh = new manifold.Mesh(meshData);
    const manifoldShape = new manifold.Manifold(mesh);
    return new MeshShape(manifoldShape);
  }
  shell(thicknessOrConfig, toleranceOrFinderFcn = null, tolerance = 0.001) {
    const tol = typeof toleranceOrFinderFcn === "number" ? toleranceOrFinderFcn : tolerance;
    let filter;
    let thickness;
    if (typeof thicknessOrConfig === "number") {
      thickness = thicknessOrConfig;
      const ff = new FaceFinder;
      filter = typeof toleranceOrFinderFcn === "function" ? toleranceOrFinderFcn(ff) : ff;
    } else {
      thickness = thicknessOrConfig.thickness;
      filter = thicknessOrConfig.filter;
    }
    const r = GCWithScope();
    const filteredFaces = filter.find(this);
    const facesToRemove = r(new this.oc.TopTools_ListOfShape_1);
    filteredFaces.forEach((face) => {
      facesToRemove.Append_1(face.wrapped);
    });
    const progress = r(new this.oc.Message_ProgressRange_1);
    const shellBuilder = r(new this.oc.BRepOffsetAPI_MakeThickSolid);
    shellBuilder.MakeThickSolidByJoin(this.wrapped, facesToRemove, -thickness, tol, this.oc.BRepOffset_Mode.BRepOffset_Skin, false, false, this.oc.GeomAbs_JoinType.GeomAbs_Arc, false, progress);
    const newShape = cast(shellBuilder.Shape());
    if (!isShape3D(newShape))
      throw new Error("Could not shell as a 3d shape");
    return newShape;
  }
  _builderIter(radiusConfigInput, builderAdd, isRadius) {
    if (isRadius(radiusConfigInput)) {
      let edgeCount = 0;
      for (const rawEdge of this._iterTopo("edge")) {
        builderAdd(radiusConfigInput, downcast(rawEdge));
        edgeCount += 1;
      }
      return edgeCount;
    }
    let radiusConfigFun;
    let finalize = null;
    if (typeof radiusConfigInput === "function") {
      radiusConfigFun = radiusConfigInput;
    } else {
      radiusConfigFun = (element) => {
        const shouldKeep = radiusConfigInput.filter.shouldKeep(element);
        return shouldKeep ? radiusConfigInput.radius || 1 : null;
      };
      if (radiusConfigInput.filter && !radiusConfigInput.keep) {
        finalize = () => radiusConfigInput.filter.delete();
      }
    }
    let edgeAddedCount = 0;
    for (const e of this._iterTopo("edge")) {
      const rawEdge = downcast(e);
      const edge = new Edge(rawEdge);
      const radius = radiusConfigFun(edge);
      if (radius) {
        builderAdd(radius, rawEdge);
        edgeAddedCount += 1;
      }
      edge.delete();
    }
    finalize && finalize();
    return edgeAddedCount;
  }
  fillet(radiusConfig, filter) {
    const r = GCWithScope();
    const filletBuilder = r(new this.oc.BRepFilletAPI_MakeFillet(this.wrapped, this.oc.ChFi3d_FilletShape.ChFi3d_Rational));
    let config = radiusConfig;
    if (isFilletRadius(radiusConfig) && filter) {
      config = {
        radius: radiusConfig,
        filter: filter(new EdgeFinder)
      };
    }
    const edgesFound = this._builderIter(config, (r2, e) => {
      if (isNumber(r2))
        return filletBuilder.Add_2(r2, e);
      console.log(e);
      return filletBuilder.Add_3(r2[0], r2[1], e);
    }, isFilletRadius);
    if (!edgesFound)
      throw new Error("Could not fillet, no edge was selected");
    const newShape = cast(filletBuilder.Shape());
    if (!isShape3D(newShape))
      throw new Error("Could not fillet as a 3d shape");
    return newShape;
  }
  chamfer(radiusConfig, filter) {
    const r = GCWithScope();
    const chamferBuilder = r(new this.oc.BRepFilletAPI_MakeChamfer(this.wrapped));
    let config = radiusConfig;
    if (isChamferRadius(radiusConfig) && filter) {
      config = {
        radius: radiusConfig,
        filter: filter(new EdgeFinder)
      };
    }
    const edgesFound = this._builderIter(config, (r2, e) => {
      if (isNumber(r2))
        return chamferBuilder.Add_2(r2, e);
      const finder = new FaceFinder;
      const face = r2.selectedFace(finder).find(this, { unique: true });
      if (!face)
        throw new Error("Could not find face for chamfer");
      if ("distances" in r2) {
        return chamferBuilder.Add_3(r2.distances[0] ?? 1, r2.distances[1] ?? 1, e, face.wrapped);
      }
      if ("distance" in r2) {
        return chamferBuilder.AddDA(r2.distance, r2.angle * DEG2RAD, e, face.wrapped);
      }
    }, isChamferRadius);
    if (!edgesFound)
      throw new Error("Could not chamfer, no edge was selected");
    const newShape = cast(chamferBuilder.Shape());
    if (!isShape3D(newShape))
      throw new Error("Could not chamfer as a 3d shape");
    return newShape;
  }
  draft(angle, faceFinder, neutralPlane = "XY") {
    const oc = getOC();
    const drafter = new oc.BRepOffsetAPI_DraftAngle_2(this.wrapped);
    const inputPlane = makePlane(neutralPlane);
    const plane = makePln(inputPlane.origin, inputPlane.zDir);
    const dir = asDir(inputPlane.zDir);
    const faces = faceFinder(new FaceFinder).find(this);
    faces.forEach((f) => drafter.Add(f.wrapped, dir, angle * DEG2RAD, plane, false));
    drafter.Build(new oc.Message_ProgressRange_1);
    const newShape = drafter.ModifiedShape(this.wrapped);
    drafter.delete();
    plane.delete();
    dir.delete();
    return cast(newShape);
  }
}

class Shell extends _3DShape {
}

class Solid extends _3DShape {
}

class CompSolid extends _3DShape {
}

class Compound extends _3DShape {
}
function isShape3D(shape) {
  return shape instanceof Shell || shape instanceof Solid || shape instanceof CompSolid || shape instanceof Compound;
}
function isWire(shape) {
  return shape instanceof Wire;
}
function downcast(shape) {
  const oc = getOC();
  const ta = oc.TopAbs_ShapeEnum;
  const CAST_MAP = /* @__PURE__ */ new Map([
    [ta.TopAbs_VERTEX, oc.TopoDS.Vertex_1],
    [ta.TopAbs_EDGE, oc.TopoDS.Edge_1],
    [ta.TopAbs_WIRE, oc.TopoDS.Wire_1],
    [ta.TopAbs_FACE, oc.TopoDS.Face_1],
    [ta.TopAbs_SHELL, oc.TopoDS.Shell_1],
    [ta.TopAbs_SOLID, oc.TopoDS.Solid_1],
    [ta.TopAbs_COMPSOLID, oc.TopoDS.CompSolid_1],
    [ta.TopAbs_COMPOUND, oc.TopoDS.Compound_1]
  ]);
  const myType = shapeType(shape);
  const caster = CAST_MAP.get(myType);
  if (!caster)
    throw new Error("Could not find a wrapper for this shape type");
  return caster(shape);
}
function cast(shape) {
  const oc = getOC();
  const ta = oc.TopAbs_ShapeEnum;
  const CAST_MAP = /* @__PURE__ */ new Map([
    [ta.TopAbs_VERTEX, Vertex],
    [ta.TopAbs_EDGE, Edge],
    [ta.TopAbs_WIRE, Wire],
    [ta.TopAbs_FACE, Face],
    [ta.TopAbs_SHELL, Shell],
    [ta.TopAbs_SOLID, Solid],
    [ta.TopAbs_COMPSOLID, CompSolid],
    [ta.TopAbs_COMPOUND, Compound]
  ]);
  const Klass = CAST_MAP.get(shapeType(shape));
  if (!Klass)
    throw new Error(`Could not find a wrapper for this shape type`);
  return new Klass(downcast(shape));
}
var makeLine = (v1, v2) => {
  const oc = getOC();
  return new Edge(new oc.BRepBuilderAPI_MakeEdge_3(asPnt(v1), asPnt(v2)).Edge());
};
var makeHelix = (pitch, height, radius, center = [0, 0, 0], dir = [0, 0, 1], lefthand = false) => {
  const oc = getOC();
  const [r, gc] = localGC();
  let myDir = 2 * Math.PI;
  if (lefthand) {
    myDir = -2 * Math.PI;
  }
  const geomLine = r(new oc.Geom2d_Line_3(r(new oc.gp_Pnt2d_3(0, 0)), r(new oc.gp_Dir2d_4(myDir, pitch))));
  const nTurns = height / pitch;
  const uStart = geomLine.Value(0);
  const uStop = geomLine.Value(nTurns * Math.sqrt((2 * Math.PI) ** 2 + pitch ** 2));
  const geomSeg = r(new oc.GCE2d_MakeSegment_1(uStart, uStop));
  const geomSurf = new oc.Geom_CylindricalSurface_1(r(makeAx3(center, dir)), radius);
  const e = r(new oc.BRepBuilderAPI_MakeEdge_30(r(new oc.Handle_Geom2d_Curve_2(geomSeg.Value().get())), r(new oc.Handle_Geom_Surface_2(geomSurf)))).Edge();
  const w = r(new oc.BRepBuilderAPI_MakeWire_2(e)).Wire();
  oc.BRepLib.BuildCurves3d_2(w);
  gc();
  return new Wire(w);
};
var assembleWire = (listOfEdges) => {
  const oc = getOC();
  const wireBuilder = new oc.BRepBuilderAPI_MakeWire_1;
  listOfEdges.forEach((e) => {
    if (e instanceof Edge) {
      wireBuilder.Add_1(e.wrapped);
    }
    if (e instanceof Wire) {
      wireBuilder.Add_2(e.wrapped);
    }
  });
  const progress = new oc.Message_ProgressRange_1;
  wireBuilder.Build(progress);
  const res = wireBuilder.Error();
  if (res !== oc.BRepBuilderAPI_WireError.BRepBuilderAPI_WireDone) {
    const errorNames = /* @__PURE__ */ new Map([
      [oc.BRepBuilderAPI_WireError.BRepBuilderAPI_EmptyWire, "empty wire"],
      [
        oc.BRepBuilderAPI_WireError.BRepBuilderAPI_NonManifoldWire,
        "non manifold wire"
      ],
      [
        oc.BRepBuilderAPI_WireError.BRepBuilderAPI_DisconnectedWire,
        "disconnected wire"
      ]
    ]);
    throw new Error(`Failed to build the wire, ${errorNames.get(res) || "unknown error"}`);
  }
  const wire = new Wire(wireBuilder.Wire());
  wireBuilder.delete();
  progress.delete();
  return wire;
};
var makeFace = (wire, holes) => {
  const oc = getOC();
  const faceBuilder = new oc.BRepBuilderAPI_MakeFace_15(wire.wrapped, false);
  holes == null || holes.forEach((hole) => {
    faceBuilder.Add(hole.wrapped);
  });
  if (!faceBuilder.IsDone()) {
    faceBuilder.delete();
    throw new Error("Failed to build the face. Your wire might be non planar.");
  }
  const face = faceBuilder.Face();
  faceBuilder.delete();
  return new Face(face);
};
var makeNewFaceWithinFace = (originFace, wire) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const surface = r(oc.BRep_Tool.Surface_2(originFace.wrapped));
  const faceBuilder = r(new oc.BRepBuilderAPI_MakeFace_21(surface, wire.wrapped, true));
  const face = faceBuilder.Face();
  gc();
  return new Face(face);
};
var makeCylinder = (radius, height, location = [0, 0, 0], direction = [0, 0, 1]) => {
  const oc = getOC();
  const axis = makeAx2(location, direction);
  const cylinder = new oc.BRepPrimAPI_MakeCylinder_3(axis, radius, height);
  const solid = new Solid(cylinder.Shape());
  axis.delete();
  cylinder.delete();
  return solid;
};
var makeSphere = (radius) => {
  const oc = getOC();
  const sphereMaker = new oc.BRepPrimAPI_MakeSphere_1(radius);
  const sphere = new Solid(sphereMaker.Shape());
  sphereMaker.delete();
  return sphere;
};
var makeBox = (corner1, corner2) => {
  const oc = getOC();
  const boxMaker = new oc.BRepPrimAPI_MakeBox_4(asPnt(corner1), asPnt(corner2));
  const box = new Solid(boxMaker.Solid());
  boxMaker.delete();
  return box;
};
var makeVertex = (point) => {
  const oc = getOC();
  const pnt2 = asPnt(point);
  const vertexMaker = new oc.BRepBuilderAPI_MakeVertex(pnt2);
  const vertex = vertexMaker.Vertex();
  vertexMaker.delete();
  return new Vertex(vertex);
};
var compoundShapes = (shapeArray) => {
  const oc = getOC();
  const builder = new oc.TopoDS_Builder;
  const compound = new oc.TopoDS_Compound;
  builder.MakeCompound(compound);
  shapeArray.forEach((s) => {
    builder.Add(compound, s.wrapped);
    s.delete();
  });
  const newShape = cast(compound);
  return newShape;
};
var makeCompound = compoundShapes;
function _weld(facesOrShells) {
  const oc = getOC();
  const r = GCWithScope();
  const shellBuilder = r(new oc.BRepBuilderAPI_Sewing(0.000001, true, true, true, false));
  facesOrShells.forEach(({ wrapped }) => {
    shellBuilder.Add(wrapped);
  });
  shellBuilder.Perform(r(new oc.Message_ProgressRange_1));
  return cast(downcast(shellBuilder.SewedShape()));
}
function makeSolid(facesOrShells) {
  const r = GCWithScope();
  const oc = getOC();
  const shell = _weld(facesOrShells);
  const solid = cast(r(new oc.ShapeFix_Solid_1).SolidFromShell(shell.wrapped));
  if (!(solid instanceof Solid))
    throw new Error("Could not make a solid of faces and shells");
  return solid;
}
var addHolesInFace = (face, holes) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const faceMaker = r(new oc.BRepBuilderAPI_MakeFace_2(face.wrapped));
  holes.forEach((wire) => {
    faceMaker.Add(wire.wrapped);
  });
  const builtFace = r(faceMaker.Face());
  const fixer = r(new oc.ShapeFix_Face_2(builtFace));
  fixer.FixOrientation_1();
  const newFace = fixer.Face();
  gc();
  return new Face(newFace);
};
var isTangent = (c) => c === "symmetric" || typeof c === "number" || Array.isArray(c) && c.length === 2;
var defaultsSplineConfig = (config) => {
  let conf;
  if (!config)
    conf = { endTangent: [1, 0] };
  else if (isTangent(config)) {
    conf = { endTangent: config };
  } else {
    conf = { endTangent: 0, ...config };
  }
  const {
    endTangent: endTgt,
    startFactor = 1,
    endFactor = 1,
    startTangent: startTgt
  } = conf;
  let endTangent;
  if (typeof endTgt === "number") {
    endTangent = polarToCartesian(1, endTgt * DEG2RAD);
  } else {
    endTangent = endTgt;
  }
  let startTangent;
  if (typeof startTgt === "number") {
    startTangent = polarToCartesian(1, startTgt * DEG2RAD);
  } else {
    startTangent = startTgt;
  }
  return { endTangent, startFactor, endFactor, startTangent };
};
function radianAngle(ux, uy, vx, vy) {
  const dot = ux * vx + uy * vy;
  const mod = Math.sqrt((ux * ux + uy * uy) * (vx * vx + vy * vy));
  let rad = Math.acos(dot / mod);
  if (ux * vy - uy * vx < 0) {
    rad = -rad;
  }
  return rad;
}
function convertSvgEllipseParams([x1, y1], [x2, y2], rx, ry, phi, fA, fS) {
  const PIx2 = Math.PI * 2;
  if (rx < 0) {
    rx = -rx;
  }
  if (ry < 0) {
    ry = -ry;
  }
  if (rx == 0 || ry == 0) {
    throw Error("rx and ry can not be 0");
  }
  const s_phi = Math.sin(phi);
  const c_phi = Math.cos(phi);
  const hd_x = (x1 - x2) / 2;
  const hd_y = (y1 - y2) / 2;
  const hs_x = (x1 + x2) / 2;
  const hs_y = (y1 + y2) / 2;
  const x1_ = c_phi * hd_x + s_phi * hd_y;
  const y1_ = c_phi * hd_y - s_phi * hd_x;
  const lambda = x1_ * x1_ / (rx * rx) + y1_ * y1_ / (ry * ry);
  if (lambda > 1) {
    rx = rx * Math.sqrt(lambda);
    ry = ry * Math.sqrt(lambda);
  }
  const rxry = rx * ry;
  const rxy1_ = rx * y1_;
  const ryx1_ = ry * x1_;
  const sum_of_sq = rxy1_ * rxy1_ + ryx1_ * ryx1_;
  if (!sum_of_sq) {
    throw Error("start point can not be same as end point");
  }
  let coe = Math.sqrt(Math.abs((rxry * rxry - sum_of_sq) / sum_of_sq));
  if (fA == fS) {
    coe = -coe;
  }
  const cx_ = coe * rxy1_ / ry;
  const cy_ = -coe * ryx1_ / rx;
  const cx = c_phi * cx_ - s_phi * cy_ + hs_x;
  const cy = s_phi * cx_ + c_phi * cy_ + hs_y;
  const xcr1 = (x1_ - cx_) / rx;
  const xcr2 = (x1_ + cx_) / rx;
  const ycr1 = (y1_ - cy_) / ry;
  const ycr2 = (y1_ + cy_) / ry;
  const startAngle = radianAngle(1, 0, xcr1, ycr1);
  let deltaAngle = radianAngle(xcr1, ycr1, -xcr2, -ycr2);
  while (deltaAngle > PIx2) {
    deltaAngle -= PIx2;
  }
  while (deltaAngle < 0) {
    deltaAngle += PIx2;
  }
  if (!fS) {
    deltaAngle -= PIx2;
  }
  let endAngle = startAngle + deltaAngle;
  while (endAngle > PIx2) {
    endAngle -= PIx2;
  }
  while (endAngle < 0) {
    endAngle += PIx2;
  }
  const outputObj = {
    cx,
    cy,
    startAngle,
    deltaAngle,
    endAngle,
    clockwise: !!fS,
    rx,
    ry
  };
  return outputObj;
}
var basicFaceExtrusion = (face, extrusionVec) => {
  const oc = getOC();
  const solidBuilder = new oc.BRepPrimAPI_MakePrism_1(face.wrapped, extrusionVec.wrapped, false, true);
  const solid = new Solid(downcast(solidBuilder.Shape()));
  solidBuilder.delete();
  return solid;
};
var revolution = (face, center = [0, 0, 0], direction = [0, 0, 1], angle = 360) => {
  const oc = getOC();
  const ax = makeAx1(center, direction);
  const revolBuilder = new oc.BRepPrimAPI_MakeRevol_1(face.wrapped, ax, angle * DEG2RAD, false);
  const shape = cast(revolBuilder.Shape());
  ax.delete();
  revolBuilder.delete();
  if (!isShape3D(shape))
    throw new Error("Could not revolve to a 3d shape");
  return shape;
};
function genericSweep(wire, spine, {
  frenet = false,
  auxiliarySpine,
  law = null,
  transitionMode = "right",
  withContact,
  support,
  forceProfileSpineOthogonality
} = {}, shellMode = false) {
  const oc = getOC();
  const withCorrection = transitionMode === "round" ? true : !!forceProfileSpineOthogonality;
  const sweepBuilder = new oc.BRepOffsetAPI_MakePipeShell(spine.wrapped);
  if (transitionMode) {
    const mode = {
      transformed: oc.BRepBuilderAPI_TransitionMode.BRepBuilderAPI_Transformed,
      round: oc.BRepBuilderAPI_TransitionMode.BRepBuilderAPI_RoundCorner,
      right: oc.BRepBuilderAPI_TransitionMode.BRepBuilderAPI_RightCorner
    }[transitionMode];
    if (mode)
      sweepBuilder.SetTransitionMode(mode);
  }
  if (support) {
    sweepBuilder.SetMode_4(support);
  } else if (frenet) {
    sweepBuilder.SetMode_1(frenet);
  }
  if (auxiliarySpine) {
    sweepBuilder.SetMode_5(auxiliarySpine.wrapped, false, oc.BRepFill_TypeOfContact.BRepFill_NoContact);
  }
  if (!law)
    sweepBuilder.Add_1(wire.wrapped, !!withContact, withCorrection);
  else
    sweepBuilder.SetLaw_1(wire.wrapped, law, !!withContact, withCorrection);
  const progress = new oc.Message_ProgressRange_1;
  sweepBuilder.Build(progress);
  if (!shellMode) {
    sweepBuilder.MakeSolid();
  }
  const shape = cast(sweepBuilder.Shape());
  if (!isShape3D(shape))
    throw new Error("Could not sweep to a 3d shape");
  if (shellMode) {
    const startWire = cast(sweepBuilder.FirstShape());
    const endWire = cast(sweepBuilder.LastShape());
    if (!isWire(startWire))
      throw new Error("Could not sweep with one start wire");
    if (!isWire(endWire))
      throw new Error("Could not sweep with one end wire");
    sweepBuilder.delete();
    return [shape, startWire, endWire];
  }
  sweepBuilder.delete();
  progress.delete();
  return shape;
}
var buildLawFromProfile = (extrusionLength, { profile, endFactor = 1 }) => {
  let law;
  const oc = getOC();
  if (profile === "s-curve") {
    law = new oc.Law_S;
    law.Set_1(0, 1, extrusionLength, endFactor);
  } else if (profile === "linear") {
    law = new oc.Law_Linear;
    law.Set(0, 1, extrusionLength, endFactor);
  } else {
    throw new Error("Could not generate a law function");
  }
  return law.Trim(0, extrusionLength, 0.000001);
};
function complexExtrude(wire, center, normal, profileShape, shellMode = false) {
  const centerVec = new Vector(center);
  const normalVec = new Vector(normal);
  const mainSpineEdge = makeLine(centerVec, centerVec.add(normalVec));
  const spine = assembleWire([mainSpineEdge]);
  const law = profileShape ? buildLawFromProfile(normalVec.Length, profileShape) : null;
  const shape = shellMode ? genericSweep(wire, spine, { law }, shellMode) : genericSweep(wire, spine, { law }, shellMode);
  return shape;
}
function twistExtrude(wire, angleDegrees, center, normal, profileShape, shellMode = false) {
  const centerVec = new Vector(center);
  const normalVec = new Vector(normal);
  const mainSpineEdge = makeLine(centerVec, centerVec.add(normalVec));
  const spine = assembleWire([mainSpineEdge]);
  const pitch = 360 / angleDegrees * normalVec.Length;
  const radius = 1;
  const auxiliarySpine = makeHelix(pitch, normalVec.Length, radius, center, normal);
  const law = profileShape ? buildLawFromProfile(normalVec.Length, profileShape) : null;
  const shape = shellMode ? genericSweep(wire, spine, { auxiliarySpine, law }, shellMode) : genericSweep(wire, spine, { auxiliarySpine, law }, shellMode);
  return shape;
}
var loft = (wires, { ruled = true, startPoint, endPoint } = {}, returnShell = false) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const loftBuilder = r(new oc.BRepOffsetAPI_ThruSections(!returnShell, ruled, 0.000001));
  if (startPoint) {
    loftBuilder.AddVertex(r(makeVertex(startPoint)).wrapped);
  }
  wires.forEach((w) => loftBuilder.AddWire(w.wrapped));
  if (endPoint) {
    loftBuilder.AddVertex(r(makeVertex(endPoint)).wrapped);
  }
  const progress = r(new oc.Message_ProgressRange_1);
  loftBuilder.Build(progress);
  const shape = cast(loftBuilder.Shape());
  gc();
  if (!isShape3D(shape))
    throw new Error("Could not loft to a 3d shape");
  return shape;
};

class Sketch {
  constructor(wire, {
    defaultOrigin = [0, 0, 0],
    defaultDirection = [0, 0, 1]
  } = {}) {
    this.wire = wire;
    this.defaultOrigin = defaultOrigin;
    this.defaultDirection = defaultDirection;
    this.baseFace = null;
  }
  get baseFace() {
    return this._baseFace;
  }
  set baseFace(newFace) {
    if (!newFace)
      this._baseFace = newFace;
    else
      this._baseFace = newFace.clone();
  }
  delete() {
    this.wire.delete();
    this._defaultOrigin.delete();
    this._defaultDirection.delete();
    this.baseFace && this.baseFace.delete();
  }
  clone() {
    const sketch = new Sketch(this.wire.clone(), {
      defaultOrigin: this.defaultOrigin,
      defaultDirection: this.defaultDirection
    });
    if (this.baseFace)
      sketch.baseFace = this.baseFace.clone();
    return sketch;
  }
  get defaultOrigin() {
    return this._defaultOrigin;
  }
  set defaultOrigin(newOrigin) {
    this._defaultOrigin = new Vector(newOrigin);
  }
  get defaultDirection() {
    return this._defaultDirection;
  }
  set defaultDirection(newDirection) {
    this._defaultDirection = new Vector(newDirection);
  }
  face() {
    let face;
    if (!this.baseFace) {
      face = makeFace(this.wire);
    } else {
      face = makeNewFaceWithinFace(this.baseFace, this.wire);
    }
    return face;
  }
  wires() {
    return this.wire.clone();
  }
  faces() {
    return this.face();
  }
  revolve(revolutionAxis, { origin, angle } = {}) {
    const face = makeFace(this.wire);
    const solid = revolution(face, origin || this.defaultOrigin, revolutionAxis, angle);
    face.delete();
    this.delete();
    return solid;
  }
  extrude(extrusionDistance, {
    extrusionDirection,
    extrusionProfile,
    twistAngle,
    origin
  } = {}) {
    const [r, gc] = localGC();
    const extrusionVec = r(new Vector(extrusionDirection || this.defaultDirection).normalized().multiply(extrusionDistance));
    if (extrusionProfile && !twistAngle) {
      const solid2 = complexExtrude(this.wire, origin || this.defaultOrigin, extrusionVec, extrusionProfile);
      gc();
      this.delete();
      return solid2;
    }
    if (twistAngle) {
      const solid2 = twistExtrude(this.wire, twistAngle, origin || this.defaultOrigin, extrusionVec, extrusionProfile);
      gc();
      this.delete();
      return solid2;
    }
    const face = makeFace(this.wire);
    const solid = basicFaceExtrusion(face, extrusionVec);
    gc();
    this.delete();
    return solid;
  }
  sweepSketch(sketchOnPlane, sweepConfig = {}) {
    const startPoint = this.wire.startPoint;
    const normal = this.wire.tangentAt(0.000000001).multiply(-1).normalize();
    const xDir = normal.cross(this.defaultDirection).multiply(-1);
    const sketch = sketchOnPlane(new Plane(startPoint, xDir, normal), startPoint);
    const config = {
      forceProfileSpineOthogonality: true,
      ...sweepConfig
    };
    if (this.baseFace) {
      config.support = this.baseFace.wrapped;
    }
    const shape = genericSweep(sketch.wire, this.wire, config);
    this.delete();
    return shape;
  }
  loftWith(otherSketches, loftConfig = {}, returnShell = false) {
    const sketchArray = Array.isArray(otherSketches) ? [this, ...otherSketches] : [this, otherSketches];
    const shape = loft(sketchArray.map((s) => s.wire), loftConfig, returnShell);
    sketchArray.forEach((s) => s.delete());
    return shape;
  }
}
var curvesBoundingBox = (curves) => {
  const oc = getOC();
  const boundBox = new oc.Bnd_Box2d;
  curves.forEach((c) => {
    oc.BndLib_Add2dCurve.Add_3(c.wrapped, 0.000001, boundBox);
  });
  return new BoundingBox2d(boundBox);
};
function curvesAsEdgesOnPlane(curves, plane) {
  const [r, gc] = localGC();
  const ax = r(makeAx2(plane.origin, plane.zDir, plane.xDir));
  const oc = getOC();
  const edges = curves.map((curve) => {
    const curve3d = oc.GeomLib.To3d(ax, curve.wrapped);
    return new Edge(new oc.BRepBuilderAPI_MakeEdge_24(curve3d).Edge());
  });
  gc();
  return edges;
}
var curvesAsEdgesOnSurface = (curves, geomSurf) => {
  const [r, gc] = localGC();
  const oc = getOC();
  const modifiedCurves = curves.map((curve) => {
    const edgeBuilder = r(new oc.BRepBuilderAPI_MakeEdge_30(curve.wrapped, geomSurf));
    return new Edge(edgeBuilder.Edge());
  });
  gc();
  return modifiedCurves;
};
var transformCurves = (curves, transformation) => {
  const oc = getOC();
  const modifiedCurves = curves.map((curve) => {
    if (!transformation)
      return curve.clone();
    return new Curve2D(oc.GeomLib.GTransform(curve.wrapped, transformation));
  });
  return modifiedCurves;
};

class Transformation2D extends WrappingObj {
  transformCurves(curves) {
    return transformCurves(curves, this.wrapped);
  }
}
var stretchTransform2d = (ratio, direction, origin = [0, 0]) => {
  const oc = getOC();
  const axis = axis2d(origin, direction);
  const transform = new oc.gp_GTrsf2d_1;
  transform.SetAffinity(axis, ratio);
  axis.delete();
  return new Transformation2D(transform);
};
var translationTransform2d = (translation) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const rotation = new oc.gp_Trsf2d_1;
  rotation.SetTranslation_1(r(vec(translation)));
  const transform = new oc.gp_GTrsf2d_2(rotation);
  gc();
  return new Transformation2D(transform);
};
var mirrorTransform2d = (centerOrDirection, origin = [0, 0], mode = "center") => {
  const oc = getOC();
  const [r, gc] = localGC();
  const rotation = new oc.gp_Trsf2d_1;
  if (mode === "center") {
    rotation.SetMirror_1(r(pnt(centerOrDirection)));
  } else {
    rotation.SetMirror_2(r(axis2d(origin, centerOrDirection)));
  }
  const transform = new oc.gp_GTrsf2d_2(rotation);
  gc();
  return new Transformation2D(transform);
};
var rotateTransform2d = (angle, center = [0, 0]) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const rotation = new oc.gp_Trsf2d_1;
  rotation.SetRotation(r(pnt(center)), angle);
  const transform = new oc.gp_GTrsf2d_2(rotation);
  gc();
  return new Transformation2D(transform);
};
var scaleTransform2d = (scaleFactor, center = [0, 0]) => {
  const oc = getOC();
  const [r, gc] = localGC();
  const scaling = new oc.gp_Trsf2d_1;
  scaling.SetScale(r(pnt(center)), scaleFactor);
  const transform = new oc.gp_GTrsf2d_2(scaling);
  gc();
  return new Transformation2D(transform);
};
function curvesAsEdgesOnFace(curves, face, scale2 = "original") {
  const [r, gc] = localGC();
  const oc = getOC();
  let geomSurf = r(oc.BRep_Tool.Surface_2(face.wrapped));
  const bounds = face.UVBounds;
  let transformation = null;
  const uAxis = r(axis2d([0, 0], [0, 1]));
  const vAxis = r(axis2d([0, 0], [1, 0]));
  if (scale2 === "original" && face.geomType !== "PLANE") {
    if (face.geomType !== "CYLINDRE")
      throw new Error("Only planar and cylidrical faces can be unwrapped for sketching");
    const cylinder = r(geomSurf.get().Cylinder());
    if (!cylinder.Direct()) {
      geomSurf = geomSurf.get().UReversed();
    }
    const radius = cylinder.Radius();
    const affinity = stretchTransform2d(1 / radius, [0, 1]);
    transformation = affinity.wrapped;
  }
  if (scale2 === "bounds") {
    transformation = r(new oc.gp_GTrsf2d_1);
    transformation.SetAffinity(uAxis, bounds.uMax - bounds.uMin);
    if (bounds.uMin !== 0) {
      const translation = r(new oc.gp_GTrsf2d_1);
      translation.SetTranslationPart(new oc.gp_XY_2(0, -bounds.uMin));
      transformation.Multiply(translation);
    }
    const vTransformation = r(new oc.gp_GTrsf2d_1);
    vTransformation.SetAffinity(vAxis, bounds.vMax - bounds.vMin);
    transformation.Multiply(vTransformation);
    if (bounds.vMin !== 0) {
      const translation = r(new oc.gp_GTrsf2d_1);
      translation.SetTranslationPart(r(new oc.gp_XY_2(0, -bounds.vMin)));
      transformation.Multiply(translation);
    }
  }
  const modifiedCurves = transformCurves(curves, transformation);
  const edges = curvesAsEdgesOnSurface(modifiedCurves, geomSurf);
  gc();
  return edges;
}
var viewbox = (bbox, margin = 1) => {
  const minX = bbox.bounds[0][0] - margin;
  const minY = -bbox.bounds[1][1] - margin;
  return `${minX} ${minY} ${bbox.width + 2 * margin} ${bbox.height + 2 * margin}`;
};
var asSVG = (body, boundingBox, margin = 1) => {
  const vbox = viewbox(boundingBox, margin);
  return `<svg version="1.1" xmlns="http://www.w3.org/2000/svg" viewBox="${vbox}" fill="none" stroke="black" stroke-width="0.6%" vector-effect="non-scaling-stroke">
    ${body}
</svg>`;
};

class Blueprint {
  constructor(curves) {
    this.curves = curves;
    this._boundingBox = null;
    this._orientation = null;
    this._guessedOrientation = null;
  }
  delete() {
    this.curves.forEach((c) => c.delete());
    if (this._boundingBox)
      this._boundingBox.delete();
  }
  clone() {
    return new Blueprint(this.curves);
  }
  get repr() {
    return ["Blueprint", ...this.curves.map((c) => c.repr)].join(`
`);
  }
  get boundingBox() {
    if (!this._boundingBox) {
      this._boundingBox = curvesBoundingBox(this.curves);
    }
    return this._boundingBox;
  }
  get orientation() {
    if (this._orientation)
      return this._orientation;
    if (this._guessedOrientation)
      return this._guessedOrientation;
    const vertices = this.curves.flatMap((c) => {
      if (c.geomType !== "LINE") {
        return [c.firstPoint, c.value(0.5)];
      }
      return [c.firstPoint];
    });
    const approximateArea = vertices.map((v1, i) => {
      const v2 = vertices[(i + 1) % vertices.length];
      return (v2[0] - v1[0]) * (v2[1] + v1[1]);
    }).reduce((a, b) => a + b, 0);
    this._guessedOrientation = approximateArea > 0 ? "clockwise" : "counterClockwise";
    return this._guessedOrientation;
  }
  stretch(ratio, direction, origin = [0, 0]) {
    const curves = stretchTransform2d(ratio, direction, origin).transformCurves(this.curves);
    return new Blueprint(curves);
  }
  scale(scaleFactor, center) {
    const centerPoint = center || this.boundingBox.center;
    const curves = scaleTransform2d(scaleFactor, centerPoint).transformCurves(this.curves);
    return new Blueprint(curves);
  }
  rotate(angle, center) {
    const curves = rotateTransform2d(angle * DEG2RAD, center).transformCurves(this.curves);
    return new Blueprint(curves);
  }
  translate(xDistOrPoint, yDist = 0) {
    const translationVector = isPoint2D(xDistOrPoint) ? xDistOrPoint : [xDistOrPoint, yDist];
    const curves = translationTransform2d(translationVector).transformCurves(this.curves);
    return new Blueprint(curves);
  }
  mirror(centerOrDirection, origin = [0, 0], mode = "center") {
    const curves = mirrorTransform2d(centerOrDirection, origin, mode).transformCurves(this.curves);
    return new Blueprint(curves);
  }
  sketchOnPlane(inputPlane, origin) {
    const plane = inputPlane instanceof Plane ? makePlane(inputPlane) : makePlane(inputPlane, origin);
    const edges = curvesAsEdgesOnPlane(this.curves, plane);
    const wire = assembleWire(edges);
    return new Sketch(wire, {
      defaultOrigin: plane.origin,
      defaultDirection: plane.zDir
    });
  }
  sketchOnFace(face, scaleMode) {
    const oc = getOC();
    const edges = curvesAsEdgesOnFace(this.curves, face, scaleMode);
    const wire = assembleWire(edges);
    oc.BRepLib.BuildCurves3d_2(wire.wrapped);
    const wireFixer = new oc.ShapeFix_Wire_2(wire.wrapped, face.wrapped, 0.000000001);
    wireFixer.FixEdgeCurves();
    wireFixer.delete();
    const sketch = new Sketch(wire);
    if (wire.isClosed) {
      const baseFace = sketch.clone().face();
      sketch.defaultOrigin = baseFace.pointOnSurface(0.5, 0.5);
      sketch.defaultDirection = baseFace.normalAt();
      if (baseFace.orientation !== face.orientation) {
        sketch.defaultDirection = sketch.defaultDirection.multiply(-1);
      }
      sketch.baseFace = face;
    } else {
      const startPoint = wire.startPoint;
      sketch.defaultOrigin = startPoint;
      sketch.defaultDirection = face.normalAt(startPoint);
      sketch.baseFace = face;
    }
    return sketch;
  }
  subFace(face, origin) {
    let subFace = this.translate(face.uvCoordinates(origin || face.center)).sketchOnFace(face, "original").face();
    if (subFace.orientation !== face.orientation) {
      subFace = subFace.flipOrientation();
    }
    return subFace;
  }
  punchHole(shape, face, {
    height = null,
    origin = null,
    draftAngle = 0
  } = {}) {
    const oc = getOC();
    const gc = GCWithScope();
    const foundFace = getSingleFace(face, shape);
    const hole = this.subFace(foundFace, origin);
    const maker = gc(new oc.BRepFeat_MakeDPrism_1(shape.wrapped, hole.wrapped, foundFace.wrapped, draftAngle * DEG2RAD, 0, false));
    if (height) {
      maker.Perform_1(height);
    } else {
      maker.PerformThruAll();
    }
    return cast(maker.Shape());
  }
  toSVGPathD() {
    const r = GCWithScope();
    const bp = this.clone().mirror([1, 0], [0, 0], "plane");
    const compatibleCurves = approximateAsSvgCompatibleCurve(bp.curves);
    const path = compatibleCurves.flatMap((c) => {
      return adaptedCurveToPathElem(r(c.adaptor()), c.lastPoint);
    });
    const [startX, startY] = bp.curves[0].firstPoint;
    return `M ${round5(startX)} ${round5(startY)} ${path.join(" ")}${bp.isClosed() ? " Z" : ""}`;
  }
  toSVGPath() {
    return `<path d="${this.toSVGPathD()}" />`;
  }
  toSVGViewBox(margin = 1) {
    return viewbox(this.boundingBox, margin);
  }
  toSVGPaths() {
    return [this.toSVGPathD()];
  }
  toSVG(margin = 1) {
    return asSVG(this.toSVGPath(), this.boundingBox, margin);
  }
  get firstPoint() {
    return this.curves[0].firstPoint;
  }
  get lastPoint() {
    return this.curves[this.curves.length - 1].lastPoint;
  }
  isInside(point) {
    if (!this.boundingBox.containsPoint(point))
      return false;
    const oc = getOC();
    const intersector = new oc.Geom2dAPI_InterCurveCurve_1;
    const segment = make2dSegmentCurve(point, this.boundingBox.outsidePoint());
    let crossCounts = 0;
    const onCurve = this.curves.find((c) => c.isOnCurve(point));
    if (onCurve)
      return false;
    this.curves.forEach((c) => {
      if (c.boundingBox.isOut(segment.boundingBox))
        return;
      intersector.Init_1(segment.wrapped, c.wrapped, 0.000000001);
      crossCounts += intersector.NbPoints();
    });
    intersector.delete();
    return !!(crossCounts % 2);
  }
  isClosed() {
    return samePoint$2(this.firstPoint, this.lastPoint);
  }
  intersects(other) {
    const oc = getOC();
    const intersector = new oc.Geom2dAPI_InterCurveCurve_1;
    if (this.boundingBox.isOut(other.boundingBox))
      return false;
    for (const myCurve of this.curves) {
      for (const otherCurve of other.curves) {
        if (myCurve.boundingBox.isOut(otherCurve.boundingBox))
          continue;
        intersector.Init_1(myCurve.wrapped, otherCurve.wrapped, 0.000000001);
        if (intersector.NbPoints() || intersector.NbSegments())
          return true;
      }
    }
    intersector.delete();
    return false;
  }
}
function buildCornerFunction(radius, mode) {
  if (typeof radius === "function")
    return radius;
  const makeFn = mode === "chamfer" ? chamferCurves : mode === "dogbone" ? dogboneFilletCurves : filletCurves;
  return (first, second) => makeFn(first, second, radius);
}

class BaseSketcher2d {
  constructor(origin = [0, 0]) {
    this.pointer = origin;
    this.firstPoint = origin;
    this._nextCorner = null;
    this.pendingCurves = [];
  }
  _convertToUV([x, y]) {
    return [x, y];
  }
  _convertFromUV([u, v]) {
    return [u, v];
  }
  get penPosition() {
    return this.pointer;
  }
  get penAngle() {
    if (this.pendingCurves.length === 0)
      return 0;
    const lastCurve = this.pendingCurves[this.pendingCurves.length - 1];
    const [dx, dy] = lastCurve.tangentAt(1);
    const angleInRadians = Math.atan2(dy, dx);
    return angleInRadians * RAD2DEG;
  }
  movePointerTo(point) {
    if (this.pendingCurves.length)
      throw new Error("You can only move the pointer if there is no curve defined");
    this.pointer = point;
    this.firstPoint = point;
    return this;
  }
  saveCurve(curve) {
    if (!this._nextCorner) {
      this.pendingCurves.push(curve);
      return;
    }
    const previousCurve = this.pendingCurves.pop();
    if (!previousCurve)
      throw new Error("bug in the custom corner algorithm");
    this.pendingCurves.push(...this._nextCorner(previousCurve, curve));
    this._nextCorner = null;
  }
  lineTo(point) {
    const curve = make2dSegmentCurve(this._convertToUV(this.pointer), this._convertToUV(point));
    this.pointer = point;
    this.saveCurve(curve);
    return this;
  }
  line(xDist, yDist) {
    return this.lineTo([this.pointer[0] + xDist, this.pointer[1] + yDist]);
  }
  vLine(distance) {
    return this.line(0, distance);
  }
  hLine(distance) {
    return this.line(distance, 0);
  }
  vLineTo(yPos) {
    return this.lineTo([this.pointer[0], yPos]);
  }
  hLineTo(xPos) {
    return this.lineTo([xPos, this.pointer[1]]);
  }
  polarLineTo([r, theta]) {
    const angleInRads = theta * DEG2RAD;
    const point = polarToCartesian(r, angleInRads);
    return this.lineTo(point);
  }
  polarLine(distance, angle) {
    const angleInRads = angle * DEG2RAD;
    const [x, y] = polarToCartesian(distance, angleInRads);
    return this.line(x, y);
  }
  tangentLine(distance) {
    const previousCurve = this.pendingCurves.length ? this.pendingCurves[this.pendingCurves.length - 1] : null;
    if (!previousCurve)
      throw new Error("You need a previous curve to sketch a tangent line");
    const direction = normalize2d(this._convertFromUV(previousCurve.tangentAt(1)));
    return this.line(direction[0] * distance, direction[1] * distance);
  }
  threePointsArcTo(end, midPoint) {
    this.saveCurve(make2dThreePointArc(this._convertToUV(this.pointer), this._convertToUV(midPoint), this._convertToUV(end)));
    this.pointer = end;
    return this;
  }
  threePointsArc(xDist, yDist, viaXDist, viaYDist) {
    const [x0, y0] = this.pointer;
    return this.threePointsArcTo([x0 + xDist, y0 + yDist], [x0 + viaXDist, y0 + viaYDist]);
  }
  sagittaArcTo(end, sagitta) {
    const [x0, y0] = this.pointer;
    const [x1, y1] = end;
    const midPoint = [(x0 + x1) / 2, (y0 + y1) / 2];
    const sagDir = [-(y1 - y0), x1 - x0];
    const sagDirLen = Math.sqrt(sagDir[0] ** 2 + sagDir[1] ** 2);
    const sagPoint = [
      midPoint[0] + sagDir[0] / sagDirLen * sagitta,
      midPoint[1] + sagDir[1] / sagDirLen * sagitta
    ];
    this.saveCurve(make2dThreePointArc(this._convertToUV(this.pointer), this._convertToUV(sagPoint), this._convertToUV(end)));
    this.pointer = end;
    return this;
  }
  sagittaArc(xDist, yDist, sagitta) {
    return this.sagittaArcTo([xDist + this.pointer[0], yDist + this.pointer[1]], sagitta);
  }
  vSagittaArc(distance, sagitta) {
    return this.sagittaArc(0, distance, sagitta);
  }
  hSagittaArc(distance, sagitta) {
    return this.sagittaArc(distance, 0, sagitta);
  }
  bulgeArcTo(end, bulge) {
    if (!bulge)
      return this.lineTo(end);
    const halfChord = distance2d(this.pointer, end) / 2;
    const bulgeAsSagitta = -bulge * halfChord;
    return this.sagittaArcTo(end, bulgeAsSagitta);
  }
  bulgeArc(xDist, yDist, bulge) {
    return this.bulgeArcTo([xDist + this.pointer[0], yDist + this.pointer[1]], bulge);
  }
  vBulgeArc(distance, bulge) {
    return this.bulgeArc(0, distance, bulge);
  }
  hBulgeArc(distance, bulge) {
    return this.bulgeArc(distance, 0, bulge);
  }
  tangentArcTo(end) {
    const previousCurve = this.pendingCurves.length ? this.pendingCurves[this.pendingCurves.length - 1] : null;
    if (!previousCurve)
      throw new Error("You need a previous curve to sketch a tangent arc");
    this.saveCurve(make2dTangentArc(this._convertToUV(this.pointer), previousCurve.tangentAt(1), this._convertToUV(end)));
    this.pointer = end;
    return this;
  }
  tangentArc(xDist, yDist) {
    const [x0, y0] = this.pointer;
    return this.tangentArcTo([xDist + x0, yDist + y0]);
  }
  ellipseTo(end, horizontalRadius, verticalRadius, rotation = 0, longAxis = false, sweep = false) {
    let rotationAngle = rotation;
    let majorRadius = horizontalRadius;
    let minorRadius = verticalRadius;
    if (horizontalRadius < verticalRadius) {
      rotationAngle = rotation + 90;
      majorRadius = verticalRadius;
      minorRadius = horizontalRadius;
    }
    const radRotationAngle = rotationAngle * DEG2RAD;
    const convertAxis = (ax) => distance2d(this._convertToUV(ax));
    const r1 = convertAxis(polarToCartesian(majorRadius, radRotationAngle));
    const r2 = convertAxis(polarToCartesian(minorRadius, radRotationAngle + Math.PI / 2));
    const xDir = normalize2d(this._convertToUV(rotate2d([1, 0], radRotationAngle)));
    const [, newRotationAngle] = cartesianToPolar(xDir);
    const { cx, cy, startAngle, endAngle, clockwise, rx, ry } = convertSvgEllipseParams(this._convertToUV(this.pointer), this._convertToUV(end), r1, r2, newRotationAngle, longAxis, sweep);
    const arc = make2dEllipseArc(rx, ry, clockwise ? startAngle : endAngle, clockwise ? endAngle : startAngle, [cx, cy], xDir);
    if (!clockwise) {
      arc.reverse();
    }
    this.saveCurve(arc);
    this.pointer = end;
    return this;
  }
  ellipse(xDist, yDist, horizontalRadius, verticalRadius, rotation = 0, longAxis = false, sweep = false) {
    const [x0, y0] = this.pointer;
    return this.ellipseTo([xDist + x0, yDist + y0], horizontalRadius, verticalRadius, rotation, longAxis, sweep);
  }
  halfEllipseTo(end, minorRadius, sweep = false) {
    const angle = polarAngle2d(end, this.pointer);
    const distance = distance2d(end, this.pointer);
    return this.ellipseTo(end, distance / 2, minorRadius, angle * RAD2DEG, true, sweep);
  }
  halfEllipse(xDist, yDist, minorRadius, sweep = false) {
    const [x0, y0] = this.pointer;
    return this.halfEllipseTo([x0 + xDist, y0 + yDist], minorRadius, sweep);
  }
  bezierCurveTo(end, controlPoints) {
    let cp;
    if (controlPoints.length === 2 && !Array.isArray(controlPoints[0])) {
      cp = [controlPoints];
    } else {
      cp = controlPoints;
    }
    this.saveCurve(make2dBezierCurve(this._convertToUV(this.pointer), cp.map((point) => this._convertToUV(point)), this._convertToUV(end)));
    this.pointer = end;
    return this;
  }
  quadraticBezierCurveTo(end, controlPoint) {
    return this.bezierCurveTo(end, [controlPoint]);
  }
  cubicBezierCurveTo(end, startControlPoint, endControlPoint) {
    return this.bezierCurveTo(end, [startControlPoint, endControlPoint]);
  }
  smoothSplineTo(end, config) {
    const { endTangent, startTangent, startFactor, endFactor } = defaultsSplineConfig(config);
    const previousCurve = this.pendingCurves.length ? this.pendingCurves[this.pendingCurves.length - 1] : null;
    const defaultDistance = distance2d(this.pointer, end) * 0.25;
    let startPoleDirection;
    if (startTangent) {
      startPoleDirection = startTangent;
    } else if (!previousCurve) {
      startPoleDirection = [1, 0];
    } else {
      startPoleDirection = this._convertFromUV(previousCurve.tangentAt(1));
    }
    startPoleDirection = normalize2d(startPoleDirection);
    const startControl = [
      this.pointer[0] + startPoleDirection[0] * startFactor * defaultDistance,
      this.pointer[1] + startPoleDirection[1] * startFactor * defaultDistance
    ];
    let endPoleDirection;
    if (endTangent === "symmetric") {
      endPoleDirection = [-startPoleDirection[0], -startPoleDirection[1]];
    } else {
      endPoleDirection = endTangent;
    }
    endPoleDirection = normalize2d(endPoleDirection);
    const endControl = [
      end[0] - endPoleDirection[0] * endFactor * defaultDistance,
      end[1] - endPoleDirection[1] * endFactor * defaultDistance
    ];
    return this.cubicBezierCurveTo(end, startControl, endControl);
  }
  smoothSpline(xDist, yDist, splineConfig) {
    return this.smoothSplineTo([xDist + this.pointer[0], yDist + this.pointer[1]], splineConfig);
  }
  customCorner(radius, mode = "fillet") {
    if (!this.pendingCurves.length)
      throw new Error("You need a curve defined to fillet the angle");
    this._nextCorner = buildCornerFunction(radius, mode);
    return this;
  }
  _customCornerLastWithFirst(radius, mode = "fillet") {
    if (!radius)
      return;
    const previousCurve = this.pendingCurves.pop();
    const curve = this.pendingCurves.shift();
    if (!previousCurve || !curve)
      throw new Error("Not enough curves to close and fillet");
    this.pendingCurves.push(...buildCornerFunction(radius, mode)(previousCurve, curve));
  }
  _closeSketch() {
    if (!samePoint$2(this.pointer, this.firstPoint)) {
      this.lineTo(this.firstPoint);
    }
  }
  _closeWithMirror() {
    if (samePoint$2(this.pointer, this.firstPoint))
      throw new Error("Cannot close with a mirror when the sketch is already closed");
    const startToEndVector = [
      this.pointer[0] - this.firstPoint[0],
      this.pointer[1] - this.firstPoint[1]
    ];
    const mirrorAxis = axis2d(this._convertToUV(this.pointer), this._convertToUV(startToEndVector));
    const mirroredCurves = this.pendingCurves.map((c) => new Curve2D(c.innerCurve.Mirrored_2(mirrorAxis)));
    mirroredCurves.reverse();
    mirroredCurves.map((c) => c.reverse());
    this.pendingCurves.push(...mirroredCurves);
    this.pointer = this.firstPoint;
  }
}
class BlueprintSketcher extends BaseSketcher2d {
  constructor(origin = [0, 0]) {
    super();
    this.pointer = origin;
    this.firstPoint = origin;
    this.pendingCurves = [];
  }
  done() {
    return new Blueprint(this.pendingCurves);
  }
  close() {
    this._closeSketch();
    return this.done();
  }
  closeWithMirror() {
    this._closeWithMirror();
    return this.close();
  }
  closeWithCustomCorner(radius, mode = "fillet") {
    this._closeSketch();
    this._customCornerLastWithFirst(radius, mode);
    return this.done();
  }
}
var guessFaceFromWires = (wires) => {
  const oc = getOC();
  const faceBuilder = new oc.BRepOffsetAPI_MakeFilling(3, 15, 2, false, 0.00001, 0.0001, 0.01, 0.1, 8, 9);
  wires.forEach((wire, wireIndex) => {
    wire.edges.forEach((edge) => {
      faceBuilder.Add_1(edge.wrapped, oc.GeomAbs_Shape.GeomAbs_C0, wireIndex === 0);
    });
  });
  const progress = new oc.Message_ProgressRange_1;
  faceBuilder.Build(progress);
  progress.delete();
  const newFace = cast(faceBuilder.Shape());
  faceBuilder.delete();
  if (!(newFace instanceof Face)) {
    throw new Error("Failed to create a face");
  }
  return newFace;
};
var fixWire = (wire, baseFace) => {
  const oc = getOC();
  const wireFixer = new oc.ShapeFix_Wire_2(wire.wrapped, baseFace.wrapped, 0.000000001);
  wireFixer.FixEdgeCurves();
  wireFixer.delete();
  return wire;
};
var faceFromWires = (wires) => {
  let baseFace;
  let holeWires;
  try {
    baseFace = makeFace(wires[0]);
    holeWires = wires.slice(1);
  } catch (e) {
    baseFace = guessFaceFromWires(wires);
    holeWires = wires.slice(1).map((w) => fixWire(w, baseFace));
  }
  return addHolesInFace(baseFace, holeWires);
};
var solidFromShellGenerator = (sketches, shellGenerator) => {
  const shells = [];
  const startWires = [];
  const endWires = [];
  sketches.forEach((sketch) => {
    const [shell, startWire, endWire] = shellGenerator(sketch);
    shells.push(shell);
    startWires.push(startWire);
    endWires.push(endWire);
  });
  const startFace = faceFromWires(startWires);
  const endFace = faceFromWires(endWires);
  const solid = makeSolid([startFace, ...shells, endFace]);
  return solid;
};

class CompoundSketch {
  constructor(sketches) {
    this.sketches = sketches;
  }
  delete() {
    this.sketches.forEach((sketch) => sketch.delete());
  }
  get outerSketch() {
    return this.sketches[0];
  }
  get innerSketches() {
    return this.sketches.slice(1);
  }
  get wires() {
    const wires = this.sketches.map((s) => s.wire);
    return compoundShapes(wires);
  }
  face() {
    const baseFace = this.outerSketch.face();
    const newFace = addHolesInFace(baseFace, this.innerSketches.map((s) => s.wire));
    return newFace;
  }
  extrude(extrusionDistance, {
    extrusionDirection,
    extrusionProfile,
    twistAngle,
    origin
  } = {}) {
    const extrusionVec = new Vector(extrusionDirection || this.outerSketch.defaultDirection).normalized().multiply(extrusionDistance);
    if (extrusionProfile && !twistAngle) {
      const solid2 = solidFromShellGenerator(this.sketches, (sketch) => complexExtrude(sketch.wire, origin || this.outerSketch.defaultOrigin, extrusionVec, extrusionProfile, true));
      return solid2;
    }
    if (twistAngle) {
      const solid2 = solidFromShellGenerator(this.sketches, (sketch) => twistExtrude(sketch.wire, twistAngle, origin || this.outerSketch.defaultOrigin, extrusionVec, extrusionProfile, true));
      return solid2;
    }
    const solid = basicFaceExtrusion(this.face(), extrusionVec);
    return solid;
  }
  revolve(revolutionAxis, { origin, angle } = {}) {
    const solid = revolution(this.face(), origin || this.outerSketch.defaultOrigin, revolutionAxis, angle);
    return solid;
  }
  loftWith(otherCompound, loftConfig) {
    if (this.sketches.length !== otherCompound.sketches.length)
      throw new Error("You need to loft with another compound with the same number of sketches");
    const shells = this.sketches.map((base, cIndex) => {
      const outer = otherCompound.sketches[cIndex];
      return base.clone().loftWith(outer.clone(), { ruled: loftConfig.ruled }, true);
    });
    const baseFace = this.face().clone();
    shells.push(baseFace, otherCompound.face());
    return makeSolid(shells);
  }
}

class CompoundBlueprint {
  constructor(blueprints) {
    this.blueprints = blueprints;
    this._boundingBox = null;
  }
  clone() {
    return new CompoundBlueprint(this.blueprints);
  }
  get boundingBox() {
    if (!this._boundingBox) {
      const box = new BoundingBox2d;
      this.blueprints.forEach((b) => box.add(b.boundingBox));
      this._boundingBox = box;
    }
    return this._boundingBox;
  }
  get repr() {
    return [
      "Compound Blueprints",
      "-- Outline",
      this.blueprints[0].repr,
      "-- Holes",
      ...this.blueprints.slice(1).map((b) => b.repr)
    ].join(`
`);
  }
  stretch(ratio, direction, origin) {
    return new CompoundBlueprint(this.blueprints.map((bp) => bp.stretch(ratio, direction, origin)));
  }
  rotate(angle, center) {
    return new CompoundBlueprint(this.blueprints.map((bp) => bp.rotate(angle, center)));
  }
  scale(scaleFactor, center) {
    const centerPoint = center || this.boundingBox.center;
    return new CompoundBlueprint(this.blueprints.map((bp) => bp.scale(scaleFactor, centerPoint)));
  }
  translate(xDistOrPoint, yDist = 0) {
    return new CompoundBlueprint(this.blueprints.map((bp) => bp.translate(xDistOrPoint, yDist)));
  }
  mirror(centerOrDirection, origin, mode) {
    return new CompoundBlueprint(this.blueprints.map((bp) => bp.mirror(centerOrDirection, origin, mode)));
  }
  sketchOnPlane(plane, origin) {
    const sketches = this.blueprints.map((blueprint) => blueprint.sketchOnPlane(plane, origin));
    return new CompoundSketch(sketches);
  }
  sketchOnFace(face, scaleMode) {
    const sketches = this.blueprints.map((blueprint) => blueprint.sketchOnFace(face, scaleMode));
    return new CompoundSketch(sketches);
  }
  punchHole(shape, face, options = {}) {
    return this.blueprints[0].punchHole(shape, face, options);
  }
  toSVGViewBox(margin = 1) {
    return viewbox(this.boundingBox, margin);
  }
  toSVGPaths() {
    return this.blueprints.flatMap((bp) => bp.toSVGPaths());
  }
  toSVGGroup() {
    return `<g>${this.blueprints.map((b) => b.toSVGPath()).join("")}</g>`;
  }
  toSVG(margin = 1) {
    return asSVG(this.toSVGGroup(), this.boundingBox, margin);
  }
}

class Sketches {
  constructor(sketches) {
    this.sketches = sketches;
  }
  wires() {
    const wires = this.sketches.map((s) => s instanceof Sketch ? s.wire : s.wires);
    return compoundShapes(wires);
  }
  faces() {
    const faces = this.sketches.map((s) => s.face());
    return compoundShapes(faces);
  }
  extrude(extrusionDistance, extrusionConfig = {}) {
    const extruded = this.sketches.map((s) => s.extrude(extrusionDistance, extrusionConfig));
    return compoundShapes(extruded);
  }
  revolve(revolutionAxis, config) {
    return compoundShapes(this.sketches.map((s) => s.revolve(revolutionAxis, config)));
  }
}

class Blueprints {
  constructor(blueprints) {
    this.blueprints = blueprints;
    this._boundingBox = null;
  }
  get repr() {
    return ["Blueprints", ...this.blueprints.map((b) => b.repr)].join(`
`);
  }
  clone() {
    return new Blueprints(this.blueprints);
  }
  get boundingBox() {
    if (!this._boundingBox) {
      const box = new BoundingBox2d;
      this.blueprints.forEach((b) => box.add(b.boundingBox));
      this._boundingBox = box;
    }
    return this._boundingBox;
  }
  stretch(ratio, direction, origin) {
    return new Blueprints(this.blueprints.map((bp) => bp.stretch(ratio, direction, origin)));
  }
  rotate(angle, center) {
    return new Blueprints(this.blueprints.map((bp) => bp.rotate(angle, center)));
  }
  scale(scaleFactor, center) {
    const centerPoint = center || this.boundingBox.center;
    return new Blueprints(this.blueprints.map((bp) => bp.scale(scaleFactor, centerPoint)));
  }
  translate(xDistOrPoint, yDist = 0) {
    return new Blueprints(this.blueprints.map((bp) => bp.translate(xDistOrPoint, yDist)));
  }
  mirror(centerOrDirection, origin, mode) {
    return new Blueprints(this.blueprints.map((bp) => bp.mirror(centerOrDirection, origin, mode)));
  }
  sketchOnPlane(plane, origin) {
    return new Sketches(this.blueprints.map((bp) => bp.sketchOnPlane(plane, origin)));
  }
  sketchOnFace(face, scaleMode) {
    return new Sketches(this.blueprints.map((bp) => bp.sketchOnFace(face, scaleMode)));
  }
  punchHole(shape, face, options = {}) {
    let outShape = shape;
    this.blueprints.forEach((b) => {
      outShape = b.punchHole(outShape, face, options);
    });
    return outShape;
  }
  toSVGViewBox(margin = 1) {
    return viewbox(this.boundingBox, margin);
  }
  toSVGPaths() {
    return this.blueprints.map((bp) => bp.toSVGPaths());
  }
  toSVG(margin = 1) {
    const elements = this.blueprints.map((bp) => {
      if (bp instanceof Blueprint)
        return bp.toSVGPath();
      else
        return bp.toSVGGroup();
    });
    return asSVG(elements.join(`
    `), this.boundingBox, margin);
  }
}
var groupByBoundingBoxOverlap = (blueprints) => {
  const overlaps = blueprints.map((blueprint, i) => {
    return blueprints.slice(i + 1).map((v, j) => [j + i + 1, v]).filter(([, other]) => !blueprint.boundingBox.isOut(other.boundingBox)).map(([index]) => index);
  });
  const groups = [];
  const groupsInOverlaps = Array(overlaps.length);
  overlaps.forEach((indices, i) => {
    let myGroup = groupsInOverlaps[i];
    if (!myGroup) {
      myGroup = [];
      groups.push(myGroup);
    }
    myGroup.push(blueprints[i]);
    if (indices.length) {
      indices.forEach((index) => {
        groupsInOverlaps[index] = myGroup;
      });
    }
  });
  return groups;
};
var addContainmentInfo = (groupedBlueprints) => {
  return groupedBlueprints.map((blueprint, index) => {
    const firstCurve = blueprint.curves[0];
    const point = firstCurve.value((firstCurve.lastParameter + firstCurve.firstParameter) / 2);
    const isIn = groupedBlueprints.filter((potentialOuterBlueprint, j) => {
      if (index === j)
        return false;
      return potentialOuterBlueprint.isInside(point);
    });
    return {
      blueprint,
      isIn
    };
  });
};
var splitMultipleOuterBlueprints = (outerBlueprints, allBlueprints2) => {
  return outerBlueprints.flatMap(({ blueprint: outerBlueprint }) => {
    return cleanEdgeCases(allBlueprints2.filter(({ blueprint, isIn }) => blueprint === outerBlueprint || isIn.indexOf(outerBlueprint) !== -1));
  });
};
var handleNestedBlueprints = (nestedBlueprints, allBlueprints2) => {
  const firstLevelOuterBlueprints = allBlueprints2.filter(({ isIn }) => isIn.length <= 1);
  const innerLevelsBlueprints = cleanEdgeCases(addContainmentInfo(nestedBlueprints.map(({ blueprint }) => blueprint)));
  return [firstLevelOuterBlueprints, ...innerLevelsBlueprints];
};
var cleanEdgeCases = (groupedBlueprints) => {
  if (!groupedBlueprints.length)
    return [];
  const outerBlueprints = groupedBlueprints.filter(({ isIn }) => !isIn.length);
  const nestedBlueprints = groupedBlueprints.filter(({ isIn }) => isIn.length > 1);
  if (outerBlueprints.length === 1 && nestedBlueprints.length === 0) {
    return [groupedBlueprints];
  } else if (outerBlueprints.length > 1) {
    return splitMultipleOuterBlueprints(outerBlueprints, groupedBlueprints);
  } else {
    return handleNestedBlueprints(nestedBlueprints, groupedBlueprints);
  }
};
var organiseBlueprints = (blueprints) => {
  const basicGrouping = groupByBoundingBoxOverlap(blueprints).map(addContainmentInfo);
  return new Blueprints(basicGrouping.flatMap(cleanEdgeCases).map((compounds) => {
    if (compounds.length === 1)
      return compounds[0].blueprint;
    compounds.sort((a, b) => a.isIn.length - b.isIn.length);
    return new CompoundBlueprint(compounds.map(({ blueprint }) => blueprint));
  }));
};
var roundedRectangleBlueprint = (width, height, r = 0) => {
  const { rx: inputRx = 0, ry: inputRy = 0 } = typeof r === "number" ? { ry: r, rx: r } : r;
  let rx = Math.min(inputRx, width / 2);
  let ry = Math.min(inputRy, height / 2);
  const withRadius = rx && ry;
  if (!withRadius) {
    rx = 0;
    ry = 0;
  }
  const symmetricRadius = rx === ry;
  const sk = new BlueprintSketcher([
    Math.min(0, -(width / 2 - rx)),
    -height / 2
  ]);
  const addFillet = (xDist, yDist) => {
    if (withRadius) {
      if (symmetricRadius)
        sk.tangentArc(xDist, yDist);
      else
        sk.ellipse(xDist, yDist, rx, ry, 0, false, true);
    }
  };
  if (rx < width / 2) {
    sk.hLine(width - 2 * rx);
  }
  addFillet(rx, ry);
  if (ry < height / 2) {
    sk.vLine(height - 2 * ry);
  }
  addFillet(-rx, ry);
  if (rx < width / 2) {
    sk.hLine(-(width - 2 * rx));
  }
  addFillet(-rx, -ry);
  if (ry < height / 2) {
    sk.vLine(-(height - 2 * ry));
  }
  addFillet(rx, -ry);
  return sk.close();
};
var PRECISION$1 = 0.000000001;
var samePoint$1 = (x, y) => samePoint$2(x, y, PRECISION$1);
var curveMidPoint = (curve) => {
  const midParameter = (curve.lastParameter + curve.firstParameter) / 2;
  return curve.value(midParameter);
};
var rotateToStartAt = (curves, point) => {
  const startIndex = curves.findIndex((curve) => {
    return samePoint$1(point, curve.firstPoint);
  });
  const start = curves.slice(0, startIndex);
  const end = curves.slice(startIndex);
  return end.concat(start);
};
var rotateToStartAtSegment = (curves, segment) => {
  const onSegment = (curve) => {
    return samePoint$1(segment.firstPoint, curve.firstPoint) && samePoint$1(segment.lastPoint, curve.lastPoint);
  };
  let startIndex = curves.findIndex(onSegment);
  if (startIndex === -1) {
    curves = reverseSegment(curves);
    startIndex = curves.findIndex(onSegment);
    if (startIndex === -1) {
      console.error(curves.map((c) => c.repr), segment.repr);
      throw new Error("Failed to rotate to segment start");
    }
  }
  const start = curves.slice(0, startIndex);
  const end = curves.slice(startIndex);
  return end.concat(start);
};
function* createSegmentOnPoints(curves, allIntersections, allCommonSegments) {
  const endsAtIntersection = (curve) => {
    return !!allIntersections.find((intersection) => {
      return samePoint$1(intersection, curve.lastPoint);
    });
  };
  const isCommonSegment = (curve) => {
    return !!allCommonSegments.find((segment) => {
      return samePoint$1(segment.firstPoint, curve.firstPoint) && samePoint$1(segment.lastPoint, curve.lastPoint) || samePoint$1(segment.firstPoint, curve.lastPoint) && samePoint$1(segment.lastPoint, curve.firstPoint);
    });
  };
  let currentCurves = [];
  for (const curve of curves) {
    if (endsAtIntersection(curve)) {
      currentCurves.push(curve);
      yield currentCurves;
      currentCurves = [];
    } else if (isCommonSegment(curve)) {
      if (currentCurves.length) {
        yield currentCurves;
        currentCurves = [];
      }
      yield [curve];
    } else {
      currentCurves.push(curve);
    }
  }
  if (currentCurves.length) {
    yield currentCurves;
  }
}
var startOfSegment = (s) => {
  return s[0].firstPoint;
};
var endOfSegment = (s) => {
  return s[s.length - 1].lastPoint;
};
var reverseSegment = (segment) => {
  segment.reverse();
  return segment.map((curve) => {
    const newCurve = curve.clone();
    newCurve.reverse();
    return newCurve;
  });
};
var reverseSegments = (s) => {
  s.reverse();
  return s.map(reverseSegment);
};
function removeNonCrossingPoint(allIntersections, segmentedCurve, blueprintToCheck) {
  return allIntersections.filter((intersection) => {
    const segmentsOfIntersection = segmentedCurve.filter((s) => {
      return samePoint$1(s.firstPoint, intersection) || samePoint$1(s.lastPoint, intersection);
    });
    if (segmentsOfIntersection.length % 2) {
      console.error(segmentsOfIntersection, intersection);
      throw new Error("Bug in the intersection algo on non crossing point");
    }
    const isInside = segmentsOfIntersection.map((segment) => {
      return blueprintToCheck.isInside(curveMidPoint(segment));
    });
    const segmentsOnTheSameSide = isInside.every((i) => i) || !isInside.some((i) => i);
    return !segmentsOnTheSameSide;
  });
}
function blueprintsIntersectionSegments(first, second) {
  let allIntersections = [];
  const allCommonSegments = [];
  const firstCurvePoints = new Array(first.curves.length).fill(0).map(() => []);
  const secondCurvePoints = new Array(second.curves.length).fill(0).map(() => []);
  first.curves.forEach((thisCurve, firstIndex) => {
    second.curves.forEach((otherCurve, secondIndex) => {
      const { intersections, commonSegments, commonSegmentsPoints: commonSegmentsPoints2 } = intersectCurves(thisCurve, otherCurve, PRECISION$1 / 100);
      allIntersections.push(...intersections);
      firstCurvePoints[firstIndex].push(...intersections);
      secondCurvePoints[secondIndex].push(...intersections);
      allCommonSegments.push(...commonSegments);
      allIntersections.push(...commonSegmentsPoints2);
      firstCurvePoints[firstIndex].push(...commonSegmentsPoints2);
      secondCurvePoints[secondIndex].push(...commonSegmentsPoints2);
    });
  });
  allIntersections = removeDuplicatePoints(allIntersections, PRECISION$1);
  if (!allIntersections.length || allIntersections.length === 1)
    return null;
  const cutCurve = ([curve, intersections]) => {
    if (!intersections.length)
      return [curve];
    return curve.splitAt(intersections, PRECISION$1 / 100);
  };
  let firstCurveSegments = zip([first.curves, firstCurvePoints]).flatMap(cutCurve);
  let secondCurveSegments = zip([second.curves, secondCurvePoints]).flatMap(cutCurve);
  const commonSegmentsPoints = allCommonSegments.map((c) => [
    c.firstPoint,
    c.lastPoint
  ]);
  allIntersections = removeNonCrossingPoint(allIntersections, firstCurveSegments, second);
  if (!allIntersections.length && !allCommonSegments.length)
    return null;
  if (!allCommonSegments.length) {
    const startAt = allIntersections[0];
    firstCurveSegments = rotateToStartAt(firstCurveSegments, startAt);
    secondCurveSegments = rotateToStartAt(secondCurveSegments, startAt);
  } else {
    const startSegment = allCommonSegments[0];
    firstCurveSegments = rotateToStartAtSegment(firstCurveSegments, startSegment);
    secondCurveSegments = rotateToStartAtSegment(secondCurveSegments, startSegment);
  }
  const firstIntersectedSegments = Array.from(createSegmentOnPoints(firstCurveSegments, allIntersections, allCommonSegments));
  let secondIntersectedSegments = Array.from(createSegmentOnPoints(secondCurveSegments, allIntersections, allCommonSegments));
  if (!samePoint$1(endOfSegment(secondIntersectedSegments[0]), endOfSegment(firstIntersectedSegments[0])) || allCommonSegments.length > 0 && secondIntersectedSegments[0].length !== 1) {
    secondIntersectedSegments = reverseSegments(secondIntersectedSegments);
  }
  return zip([firstIntersectedSegments, secondIntersectedSegments]).map(([first2, second2]) => {
    const currentStart = startOfSegment(first2);
    const currentEnd = endOfSegment(first2);
    if (commonSegmentsPoints.find(([startPoint, endPoint]) => {
      return samePoint$1(startPoint, currentStart) && samePoint$1(endPoint, currentEnd) || samePoint$1(startPoint, currentEnd) && samePoint$1(startPoint, currentStart);
    })) {
      return [first2, "same"];
    }
    return [first2, second2];
  });
}
var splitPaths = (curves) => {
  const startPoints = curves.map((c) => c.firstPoint);
  let endPoints = curves.map((c) => c.lastPoint);
  endPoints = endPoints.slice(-1).concat(endPoints.slice(0, -1));
  const discontinuities = zip([startPoints, endPoints]).map(([startPoint, endPoint], index) => {
    if (!samePoint$1(startPoint, endPoint)) {
      return index;
    }
    return null;
  }).filter((f) => f !== null);
  if (!discontinuities.length)
    return [curves];
  const paths = zip([
    discontinuities.slice(0, -1),
    discontinuities.slice(1)
  ]).map(([start, end]) => {
    return curves.slice(start, end);
  });
  let lastPath = curves.slice(discontinuities[discontinuities.length - 1]);
  if (discontinuities[0] !== 0) {
    lastPath = lastPath.concat(curves.slice(0, discontinuities[0]));
  }
  paths.push(lastPath);
  return paths;
};
function booleanOperation(first, second, {
  firstInside,
  secondInside
}) {
  const segments = blueprintsIntersectionSegments(first, second);
  if (!segments) {
    const firstBlueprintPoint = curveMidPoint(first.curves[0]);
    const firstCurveInSecond = second.isInside(firstBlueprintPoint);
    const secondBlueprintPoint = curveMidPoint(second.curves[0]);
    const secondCurveInFirst = first.isInside(secondBlueprintPoint);
    return {
      identical: false,
      firstCurveInSecond,
      secondCurveInFirst
    };
  }
  if (segments.every(([, secondSegment]) => secondSegment === "same")) {
    return { identical: true };
  }
  let lastWasSame = null;
  let segmentsIn = null;
  const s = segments.flatMap(([firstSegment, secondSegment]) => {
    let segments2 = [];
    let segmentsOut = 0;
    if (secondSegment === "same") {
      if (segmentsIn === 1) {
        segmentsIn = 1;
        return [...firstSegment];
      }
      if (segmentsIn === 2 || segmentsIn === 0) {
        segmentsIn = null;
        return [];
      }
      if (segmentsIn === null) {
        if (!lastWasSame)
          lastWasSame = firstSegment;
        else
          lastWasSame = [...lastWasSame, ...firstSegment];
        return [];
      }
      console.error("weird situation");
      return [];
    }
    const firstSegmentPoint = curveMidPoint(firstSegment[0]);
    const firstSegmentInSecondShape = second.isInside(firstSegmentPoint);
    if (firstInside === "keep" && firstSegmentInSecondShape || firstInside === "remove" && !firstSegmentInSecondShape) {
      segmentsOut += 1;
      segments2.push(...firstSegment);
    }
    const secondSegmentPoint = curveMidPoint(secondSegment[0]);
    const secondSegmentInFirstShape = first.isInside(secondSegmentPoint);
    if (secondInside === "keep" && secondSegmentInFirstShape || secondInside === "remove" && !secondSegmentInFirstShape) {
      let segmentsToAdd = secondSegment;
      if (segmentsOut === 1) {
        segmentsToAdd = reverseSegment(secondSegment);
      }
      segmentsOut += 1;
      segments2.push(...segmentsToAdd);
    }
    if (segmentsIn === null && segmentsOut === 1 && lastWasSame) {
      segments2 = [...lastWasSame, ...segments2];
    }
    if (segmentsOut === 1) {
      segmentsIn = segmentsOut;
      lastWasSame = null;
    }
    return segments2;
  });
  const paths = splitPaths(s).filter((b) => b.length).map((b) => new Blueprint(b));
  if (paths.length === 0)
    return null;
  if (paths.length === 1)
    return paths[0];
  return organiseBlueprints(paths);
}
var fuseBlueprints = (first, second) => {
  const result = booleanOperation(first, second, {
    firstInside: "remove",
    secondInside: "remove"
  });
  if (result === null || result instanceof Blueprint || result instanceof Blueprints)
    return result;
  if (result.identical) {
    return first.clone();
  }
  if (result.firstCurveInSecond) {
    return second.clone();
  }
  if (result.secondCurveInFirst) {
    return first.clone();
  }
  return new Blueprints([first, second]);
};
var cutBlueprints = (first, second) => {
  const result = booleanOperation(first, second, {
    firstInside: "remove",
    secondInside: "keep"
  });
  if (result === null || result instanceof Blueprint || result instanceof Blueprints)
    return result;
  if (result.identical) {
    return null;
  }
  if (result.firstCurveInSecond) {
    return null;
  }
  if (result.secondCurveInFirst) {
    return new Blueprints([new CompoundBlueprint([first, second])]);
  }
  return first.clone();
};
var intersectBlueprints = (first, second) => {
  const result = booleanOperation(first, second, {
    firstInside: "keep",
    secondInside: "keep"
  });
  if (result === null || result instanceof Blueprint || result instanceof Blueprints)
    return result;
  if (result.identical) {
    return first.clone();
  }
  if (result.firstCurveInSecond) {
    return first.clone();
  }
  if (result.secondCurveInFirst) {
    return second.clone();
  }
  return null;
};
var genericIntersects = (first, second) => {
  if (first instanceof Blueprint && second instanceof Blueprint) {
    let allIntersections = [];
    first.curves.forEach((thisCurve) => {
      second.curves.forEach((otherCurve) => {
        const { intersections, commonSegmentsPoints } = intersectCurves(thisCurve, otherCurve);
        allIntersections.push(...intersections);
        allIntersections.push(...commonSegmentsPoints);
      });
    });
    allIntersections = removeDuplicatePoints(allIntersections);
    return allIntersections.length > 1;
  }
  if (first instanceof CompoundBlueprint || first instanceof Blueprints) {
    return first.blueprints.some((bp) => genericIntersects(bp, second));
  }
  if (second instanceof CompoundBlueprint || second instanceof Blueprints) {
    return second.blueprints.some((bp) => genericIntersects(first, bp));
  }
  throw new Error("Bug in the generic intersects algorithm");
};
var genericFuse = (first, second) => {
  if (first instanceof CompoundBlueprint) {
    if (second instanceof Blueprint) {
      return fuseBlueprintWithCompound(second, first);
    }
    if (second instanceof CompoundBlueprint) {
      return fuseCompoundWithCompound(first, second);
    }
  }
  if (second instanceof CompoundBlueprint) {
    if (first instanceof Blueprint) {
      return fuseBlueprintWithCompound(first, second);
    }
    if (first instanceof CompoundBlueprint) {
      return fuseCompoundWithCompound(first, second);
    }
  }
  if (first instanceof Blueprint && second instanceof Blueprint) {
    return fuseBlueprints(first, second);
  }
  throw new Error("Bug in the generic fuse algorithm");
};
var fuseIntersectingBlueprints = (blueprints) => {
  const fused = /* @__PURE__ */ new Map;
  const output = [];
  blueprints.forEach((inputBlueprint, i) => {
    let savedBlueprint;
    if (fused.has(i)) {
      savedBlueprint = fused.get(i);
    } else {
      savedBlueprint = { current: inputBlueprint, fusedWith: /* @__PURE__ */ new Set([i]) };
      output.push(savedBlueprint);
    }
    blueprints.slice(i + 1).forEach((inputOtherBlueprint, j) => {
      const blueprint = savedBlueprint.current;
      const currentIndex = i + j + 1;
      if (savedBlueprint.fusedWith.has(currentIndex))
        return;
      let otherBlueprint = inputOtherBlueprint;
      let otherIsFused = false;
      if (fused.has(currentIndex)) {
        otherBlueprint = fused.get(currentIndex).current;
        otherIsFused = true;
      }
      if (blueprint.boundingBox.isOut(otherBlueprint.boundingBox))
        return;
      if (!genericIntersects(blueprint, otherBlueprint))
        return;
      let newFused;
      if (blueprint instanceof Blueprints || otherBlueprint instanceof Blueprints) {
        newFused = fuse2D(blueprint, otherBlueprint);
      } else {
        newFused = genericFuse(blueprint, otherBlueprint);
      }
      if (!(newFused instanceof Blueprint || newFused instanceof CompoundBlueprint)) {
        if (newFused instanceof Blueprints && newFused.blueprints.length === 2) {
          return;
        } else if (newFused instanceof Blueprints && newFused.blueprints.length === 1) {
          newFused = newFused.blueprints[0];
        } else if (!(newFused instanceof Blueprints)) {
          console.error(newFused);
          throw new Error("Bug in blueprint fusing algorigthm");
        }
      }
      savedBlueprint.fusedWith.add(currentIndex);
      savedBlueprint.current = newFused;
      if (!otherIsFused)
        fused.set(currentIndex, savedBlueprint);
    });
  });
  return organiseBlueprints(output.map(({ current }) => current).filter((a) => a).flatMap((b) => allBlueprints(b)));
};
var allBlueprints = (shape) => {
  if (shape instanceof Blueprint)
    return [shape];
  if (shape instanceof CompoundBlueprint)
    return shape.blueprints;
  if (shape instanceof Blueprints)
    return shape.blueprints.flatMap((b) => allBlueprints(b));
  return [];
};
var fuseBlueprintWithCompound = (blueprint, compound) => {
  const outerFused = fuseBlueprints(blueprint, compound.blueprints[0]);
  const innerFused = compound.blueprints.slice(1).map((c) => cutBlueprints(c, blueprint));
  return organiseBlueprints([
    ...allBlueprints(outerFused),
    ...innerFused.flatMap((fused) => allBlueprints(fused))
  ]);
};
function allPairs(list1, list2) {
  const result = [];
  for (const l1 of list1) {
    for (const l2 of list2) {
      result.push([l1, l2]);
    }
  }
  return result;
}
var fuseCompoundWithCompound = (first, second) => {
  const outerFused = fuseBlueprints(first.blueprints[0], second.blueprints[0]);
  const inner1Fused = second.blueprints.slice(1).map((c) => cutBlueprints(c, first.blueprints[0]));
  const inner2Fused = first.blueprints.slice(1).map((c) => cutBlueprints(c, second.blueprints[0]));
  const innerIntersections = allPairs(first.blueprints.slice(1), second.blueprints.slice(1)).flatMap(([first2, second2]) => {
    return allBlueprints(intersectBlueprints(first2, second2));
  });
  return organiseBlueprints([
    ...allBlueprints(outerFused),
    ...inner1Fused.flatMap((fused) => allBlueprints(fused)),
    ...inner2Fused.flatMap((fused) => allBlueprints(fused)),
    ...innerIntersections
  ]);
};
var fuse2D = (first, second) => {
  if (first === null) {
    return second && second.clone();
  }
  if (second === null) {
    return first && first.clone();
  }
  if (!(first instanceof Blueprints) && second instanceof Blueprints) {
    return fuseIntersectingBlueprints([first, ...second.blueprints]);
  }
  if (!(second instanceof Blueprints) && first instanceof Blueprints) {
    return fuseIntersectingBlueprints([second, ...first.blueprints]);
  }
  if (first instanceof Blueprints && second instanceof Blueprints) {
    let out = fuse2D(first.blueprints[0], second);
    first.blueprints.slice(1).forEach((bp) => {
      out = fuse2D(bp, out);
    });
    return out;
  }
  if (first instanceof CompoundBlueprint) {
    if (second instanceof Blueprints) {
      return fuse2D(second, first);
    }
    if (second instanceof Blueprint) {
      return fuseBlueprintWithCompound(second, first);
    }
    if (second instanceof CompoundBlueprint) {
      return fuseCompoundWithCompound(first, second);
    }
  }
  if (second instanceof CompoundBlueprint) {
    if (first instanceof Blueprints) {
      return fuse2D(first, second);
    }
    if (first instanceof Blueprint) {
      return fuseBlueprintWithCompound(first, second);
    }
    if (first instanceof CompoundBlueprint) {
      return fuseCompoundWithCompound(first, second);
    }
  }
  if (first instanceof Blueprint && second instanceof Blueprint) {
    return fuseBlueprints(first, second);
  }
  return null;
};
var mergeNonIntersecting = (shapes) => {
  const exploded = shapes.flatMap((s) => {
    if (s === null)
      return [];
    if (s instanceof Blueprints)
      return s.blueprints;
    return s;
  });
  if (exploded.length === 1)
    return exploded[0];
  return new Blueprints(exploded);
};
var cut2D = (first, second) => {
  if (first === null) {
    return null;
  }
  if (second === null) {
    return first.clone();
  }
  if (first instanceof Blueprints) {
    return mergeNonIntersecting(first.blueprints.map((bp) => cut2D(bp, second)));
  }
  if (first instanceof CompoundBlueprint) {
    const wrapper = first.blueprints[0];
    if (second instanceof Blueprint && !second.intersects(wrapper)) {
      if (!wrapper.isInside(second.firstPoint))
        return null;
      const cuts = fuse2D(second, new Blueprints(first.blueprints.slice(1)));
      return organiseBlueprints([wrapper, ...allBlueprints(cuts)]);
    } else {
      let out = cut2D(first.blueprints[0], second);
      first.blueprints.slice(1).forEach((bp) => {
        out = cut2D(out, bp);
      });
      return out;
    }
  }
  if (second instanceof Blueprints) {
    return mergeNonIntersecting(second.blueprints.map((bp) => cut2D(first, bp)));
  }
  if (second instanceof CompoundBlueprint) {
    let out = cutBlueprints(first, second.blueprints[0]);
    second.blueprints.slice(1).forEach((bp) => {
      out = fuse2D(out, intersectBlueprints(bp, first));
    });
    return out;
  }
  const singleCut = cutBlueprints(first, second);
  return singleCut;
};
function intersect2D(first, second) {
  if (first === null || second === null) {
    return null;
  }
  if (first instanceof Blueprint && second instanceof Blueprint) {
    return intersectBlueprints(first, second);
  }
  if (first instanceof Blueprints) {
    return mergeNonIntersecting(first.blueprints.map((bp) => intersect2D(bp, second)));
  }
  if (first instanceof CompoundBlueprint) {
    const wrapper = first.blueprints[0];
    const cut = first.blueprints[1];
    return cut2D(intersect2D(wrapper, second), cut);
  }
  if (second instanceof Blueprints) {
    return mergeNonIntersecting(second.blueprints.map((bp) => intersect2D(first, bp)));
  }
  if (second instanceof CompoundBlueprint) {
    const wrapper = second.blueprints[0];
    const cut = second.blueprints[1];
    return cut2D(intersect2D(wrapper, first), cut);
  }
  throw new Error("intersct 2D algorithm error");
}
/*! https://mths.be/codepointat v0.2.0 by @mathias */
if (!String.prototype.codePointAt) {
  (function() {
    var defineProperty = function() {
      try {
        var object = {};
        var $defineProperty = Object.defineProperty;
        var result = $defineProperty(object, object, object) && $defineProperty;
      } catch (error) {}
      return result;
    }();
    var codePointAt = function(position) {
      if (this == null) {
        throw TypeError();
      }
      var string = String(this);
      var size = string.length;
      var index = position ? Number(position) : 0;
      if (index != index) {
        index = 0;
      }
      if (index < 0 || index >= size) {
        return;
      }
      var first = string.charCodeAt(index);
      var second;
      if (first >= 55296 && first <= 56319 && size > index + 1) {
        second = string.charCodeAt(index + 1);
        if (second >= 56320 && second <= 57343) {
          return (first - 55296) * 1024 + second - 56320 + 65536;
        }
      }
      return first;
    };
    if (defineProperty) {
      defineProperty(String.prototype, "codePointAt", {
        value: codePointAt,
        configurable: true,
        writable: true
      });
    } else {
      String.prototype.codePointAt = codePointAt;
    }
  })();
}
function Tree() {
  this.table = new Uint16Array(16);
  this.trans = new Uint16Array(288);
}
var sltree = new Tree;
var sdtree = new Tree;
var length_bits = new Uint8Array(30);
var length_base = new Uint16Array(30);
var dist_bits = new Uint8Array(30);
var dist_base = new Uint16Array(30);
var clcidx = new Uint8Array([
  16,
  17,
  18,
  0,
  8,
  7,
  9,
  6,
  10,
  5,
  11,
  4,
  12,
  3,
  13,
  2,
  14,
  1,
  15
]);
var code_tree = new Tree;
var lengths = new Uint8Array(288 + 32);
function tinf_build_bits_base(bits, base, delta, first) {
  var i, sum;
  for (i = 0;i < delta; ++i) {
    bits[i] = 0;
  }
  for (i = 0;i < 30 - delta; ++i) {
    bits[i + delta] = i / delta | 0;
  }
  for (sum = first, i = 0;i < 30; ++i) {
    base[i] = sum;
    sum += 1 << bits[i];
  }
}
function tinf_build_fixed_trees(lt, dt) {
  var i;
  for (i = 0;i < 7; ++i) {
    lt.table[i] = 0;
  }
  lt.table[7] = 24;
  lt.table[8] = 152;
  lt.table[9] = 112;
  for (i = 0;i < 24; ++i) {
    lt.trans[i] = 256 + i;
  }
  for (i = 0;i < 144; ++i) {
    lt.trans[24 + i] = i;
  }
  for (i = 0;i < 8; ++i) {
    lt.trans[24 + 144 + i] = 280 + i;
  }
  for (i = 0;i < 112; ++i) {
    lt.trans[24 + 144 + 8 + i] = 144 + i;
  }
  for (i = 0;i < 5; ++i) {
    dt.table[i] = 0;
  }
  dt.table[5] = 32;
  for (i = 0;i < 32; ++i) {
    dt.trans[i] = i;
  }
}
var offs = new Uint16Array(16);
tinf_build_fixed_trees(sltree, sdtree);
tinf_build_bits_base(length_bits, length_base, 4, 3);
tinf_build_bits_base(dist_bits, dist_base, 2, 1);
length_bits[28] = 0;
length_base[28] = 258;
function derive(v0, v1, v2, v3, t) {
  return Math.pow(1 - t, 3) * v0 + 3 * Math.pow(1 - t, 2) * t * v1 + 3 * (1 - t) * Math.pow(t, 2) * v2 + Math.pow(t, 3) * v3;
}
function BoundingBox2() {
  this.x1 = Number.NaN;
  this.y1 = Number.NaN;
  this.x2 = Number.NaN;
  this.y2 = Number.NaN;
}
BoundingBox2.prototype.isEmpty = function() {
  return isNaN(this.x1) || isNaN(this.y1) || isNaN(this.x2) || isNaN(this.y2);
};
BoundingBox2.prototype.addPoint = function(x, y) {
  if (typeof x === "number") {
    if (isNaN(this.x1) || isNaN(this.x2)) {
      this.x1 = x;
      this.x2 = x;
    }
    if (x < this.x1) {
      this.x1 = x;
    }
    if (x > this.x2) {
      this.x2 = x;
    }
  }
  if (typeof y === "number") {
    if (isNaN(this.y1) || isNaN(this.y2)) {
      this.y1 = y;
      this.y2 = y;
    }
    if (y < this.y1) {
      this.y1 = y;
    }
    if (y > this.y2) {
      this.y2 = y;
    }
  }
};
BoundingBox2.prototype.addX = function(x) {
  this.addPoint(x, null);
};
BoundingBox2.prototype.addY = function(y) {
  this.addPoint(null, y);
};
BoundingBox2.prototype.addBezier = function(x0, y0, x1, y1, x2, y2, x, y) {
  var p0 = [x0, y0];
  var p1 = [x1, y1];
  var p2 = [x2, y2];
  var p3 = [x, y];
  this.addPoint(x0, y0);
  this.addPoint(x, y);
  for (var i = 0;i <= 1; i++) {
    var b = 6 * p0[i] - 12 * p1[i] + 6 * p2[i];
    var a = -3 * p0[i] + 9 * p1[i] - 9 * p2[i] + 3 * p3[i];
    var c = 3 * p1[i] - 3 * p0[i];
    if (a === 0) {
      if (b === 0) {
        continue;
      }
      var t = -c / b;
      if (0 < t && t < 1) {
        if (i === 0) {
          this.addX(derive(p0[i], p1[i], p2[i], p3[i], t));
        }
        if (i === 1) {
          this.addY(derive(p0[i], p1[i], p2[i], p3[i], t));
        }
      }
      continue;
    }
    var b2ac = Math.pow(b, 2) - 4 * c * a;
    if (b2ac < 0) {
      continue;
    }
    var t1 = (-b + Math.sqrt(b2ac)) / (2 * a);
    if (0 < t1 && t1 < 1) {
      if (i === 0) {
        this.addX(derive(p0[i], p1[i], p2[i], p3[i], t1));
      }
      if (i === 1) {
        this.addY(derive(p0[i], p1[i], p2[i], p3[i], t1));
      }
    }
    var t2 = (-b - Math.sqrt(b2ac)) / (2 * a);
    if (0 < t2 && t2 < 1) {
      if (i === 0) {
        this.addX(derive(p0[i], p1[i], p2[i], p3[i], t2));
      }
      if (i === 1) {
        this.addY(derive(p0[i], p1[i], p2[i], p3[i], t2));
      }
    }
  }
};
BoundingBox2.prototype.addQuad = function(x0, y0, x1, y1, x, y) {
  var cp1x = x0 + 2 / 3 * (x1 - x0);
  var cp1y = y0 + 2 / 3 * (y1 - y0);
  var cp2x = cp1x + 1 / 3 * (x - x0);
  var cp2y = cp1y + 1 / 3 * (y - y0);
  this.addBezier(x0, y0, cp1x, cp1y, cp2x, cp2y, x, y);
};
function Path() {
  this.commands = [];
  this.fill = "black";
  this.stroke = null;
  this.strokeWidth = 1;
}
Path.prototype.moveTo = function(x, y) {
  this.commands.push({
    type: "M",
    x,
    y
  });
};
Path.prototype.lineTo = function(x, y) {
  this.commands.push({
    type: "L",
    x,
    y
  });
};
Path.prototype.curveTo = Path.prototype.bezierCurveTo = function(x1, y1, x2, y2, x, y) {
  this.commands.push({
    type: "C",
    x1,
    y1,
    x2,
    y2,
    x,
    y
  });
};
Path.prototype.quadTo = Path.prototype.quadraticCurveTo = function(x1, y1, x, y) {
  this.commands.push({
    type: "Q",
    x1,
    y1,
    x,
    y
  });
};
Path.prototype.close = Path.prototype.closePath = function() {
  this.commands.push({
    type: "Z"
  });
};
Path.prototype.extend = function(pathOrCommands) {
  if (pathOrCommands.commands) {
    pathOrCommands = pathOrCommands.commands;
  } else if (pathOrCommands instanceof BoundingBox2) {
    var box = pathOrCommands;
    this.moveTo(box.x1, box.y1);
    this.lineTo(box.x2, box.y1);
    this.lineTo(box.x2, box.y2);
    this.lineTo(box.x1, box.y2);
    this.close();
    return;
  }
  Array.prototype.push.apply(this.commands, pathOrCommands);
};
Path.prototype.getBoundingBox = function() {
  var box = new BoundingBox2;
  var startX = 0;
  var startY = 0;
  var prevX = 0;
  var prevY = 0;
  for (var i = 0;i < this.commands.length; i++) {
    var cmd = this.commands[i];
    switch (cmd.type) {
      case "M":
        box.addPoint(cmd.x, cmd.y);
        startX = prevX = cmd.x;
        startY = prevY = cmd.y;
        break;
      case "L":
        box.addPoint(cmd.x, cmd.y);
        prevX = cmd.x;
        prevY = cmd.y;
        break;
      case "Q":
        box.addQuad(prevX, prevY, cmd.x1, cmd.y1, cmd.x, cmd.y);
        prevX = cmd.x;
        prevY = cmd.y;
        break;
      case "C":
        box.addBezier(prevX, prevY, cmd.x1, cmd.y1, cmd.x2, cmd.y2, cmd.x, cmd.y);
        prevX = cmd.x;
        prevY = cmd.y;
        break;
      case "Z":
        prevX = startX;
        prevY = startY;
        break;
      default:
        throw new Error("Unexpected path command " + cmd.type);
    }
  }
  if (box.isEmpty()) {
    box.addPoint(0, 0);
  }
  return box;
};
Path.prototype.draw = function(ctx) {
  ctx.beginPath();
  for (var i = 0;i < this.commands.length; i += 1) {
    var cmd = this.commands[i];
    if (cmd.type === "M") {
      ctx.moveTo(cmd.x, cmd.y);
    } else if (cmd.type === "L") {
      ctx.lineTo(cmd.x, cmd.y);
    } else if (cmd.type === "C") {
      ctx.bezierCurveTo(cmd.x1, cmd.y1, cmd.x2, cmd.y2, cmd.x, cmd.y);
    } else if (cmd.type === "Q") {
      ctx.quadraticCurveTo(cmd.x1, cmd.y1, cmd.x, cmd.y);
    } else if (cmd.type === "Z") {
      ctx.closePath();
    }
  }
  if (this.fill) {
    ctx.fillStyle = this.fill;
    ctx.fill();
  }
  if (this.stroke) {
    ctx.strokeStyle = this.stroke;
    ctx.lineWidth = this.strokeWidth;
    ctx.stroke();
  }
};
Path.prototype.toPathData = function(decimalPlaces) {
  decimalPlaces = decimalPlaces !== undefined ? decimalPlaces : 2;
  function floatToString(v) {
    if (Math.round(v) === v) {
      return "" + Math.round(v);
    } else {
      return v.toFixed(decimalPlaces);
    }
  }
  function packValues() {
    var arguments$1 = arguments;
    var s = "";
    for (var i2 = 0;i2 < arguments.length; i2 += 1) {
      var v = arguments$1[i2];
      if (v >= 0 && i2 > 0) {
        s += " ";
      }
      s += floatToString(v);
    }
    return s;
  }
  var d = "";
  for (var i = 0;i < this.commands.length; i += 1) {
    var cmd = this.commands[i];
    if (cmd.type === "M") {
      d += "M" + packValues(cmd.x, cmd.y);
    } else if (cmd.type === "L") {
      d += "L" + packValues(cmd.x, cmd.y);
    } else if (cmd.type === "C") {
      d += "C" + packValues(cmd.x1, cmd.y1, cmd.x2, cmd.y2, cmd.x, cmd.y);
    } else if (cmd.type === "Q") {
      d += "Q" + packValues(cmd.x1, cmd.y1, cmd.x, cmd.y);
    } else if (cmd.type === "Z") {
      d += "Z";
    }
  }
  return d;
};
Path.prototype.toSVG = function(decimalPlaces) {
  var svg = '<path d="';
  svg += this.toPathData(decimalPlaces);
  svg += '"';
  if (this.fill && this.fill !== "black") {
    if (this.fill === null) {
      svg += ' fill="none"';
    } else {
      svg += ' fill="' + this.fill + '"';
    }
  }
  if (this.stroke) {
    svg += ' stroke="' + this.stroke + '" stroke-width="' + this.strokeWidth + '"';
  }
  svg += "/>";
  return svg;
};
Path.prototype.toDOMElement = function(decimalPlaces) {
  var temporaryPath = this.toPathData(decimalPlaces);
  var newPath = document.createElementNS("http://www.w3.org/2000/svg", "path");
  newPath.setAttribute("d", temporaryPath);
  return newPath;
};
function fail(message) {
  throw new Error(message);
}
function argument(predicate, message) {
  if (!predicate) {
    fail(message);
  }
}
var check = { fail, argument, assert: argument };
var LIMIT16 = 32768;
var LIMIT32 = 2147483648;
var decode = {};
var encode = {};
var sizeOf = {};
function constant(v) {
  return function() {
    return v;
  };
}
encode.BYTE = function(v) {
  check.argument(v >= 0 && v <= 255, "Byte value should be between 0 and 255.");
  return [v];
};
sizeOf.BYTE = constant(1);
encode.CHAR = function(v) {
  return [v.charCodeAt(0)];
};
sizeOf.CHAR = constant(1);
encode.CHARARRAY = function(v) {
  if (typeof v === "undefined") {
    v = "";
    console.warn("Undefined CHARARRAY encountered and treated as an empty string. This is probably caused by a missing glyph name.");
  }
  var b = [];
  for (var i = 0;i < v.length; i += 1) {
    b[i] = v.charCodeAt(i);
  }
  return b;
};
sizeOf.CHARARRAY = function(v) {
  if (typeof v === "undefined") {
    return 0;
  }
  return v.length;
};
encode.USHORT = function(v) {
  return [v >> 8 & 255, v & 255];
};
sizeOf.USHORT = constant(2);
encode.SHORT = function(v) {
  if (v >= LIMIT16) {
    v = -(2 * LIMIT16 - v);
  }
  return [v >> 8 & 255, v & 255];
};
sizeOf.SHORT = constant(2);
encode.UINT24 = function(v) {
  return [v >> 16 & 255, v >> 8 & 255, v & 255];
};
sizeOf.UINT24 = constant(3);
encode.ULONG = function(v) {
  return [v >> 24 & 255, v >> 16 & 255, v >> 8 & 255, v & 255];
};
sizeOf.ULONG = constant(4);
encode.LONG = function(v) {
  if (v >= LIMIT32) {
    v = -(2 * LIMIT32 - v);
  }
  return [v >> 24 & 255, v >> 16 & 255, v >> 8 & 255, v & 255];
};
sizeOf.LONG = constant(4);
encode.FIXED = encode.ULONG;
sizeOf.FIXED = sizeOf.ULONG;
encode.FWORD = encode.SHORT;
sizeOf.FWORD = sizeOf.SHORT;
encode.UFWORD = encode.USHORT;
sizeOf.UFWORD = sizeOf.USHORT;
encode.LONGDATETIME = function(v) {
  return [0, 0, 0, 0, v >> 24 & 255, v >> 16 & 255, v >> 8 & 255, v & 255];
};
sizeOf.LONGDATETIME = constant(8);
encode.TAG = function(v) {
  check.argument(v.length === 4, "Tag should be exactly 4 ASCII characters.");
  return [
    v.charCodeAt(0),
    v.charCodeAt(1),
    v.charCodeAt(2),
    v.charCodeAt(3)
  ];
};
sizeOf.TAG = constant(4);
encode.Card8 = encode.BYTE;
sizeOf.Card8 = sizeOf.BYTE;
encode.Card16 = encode.USHORT;
sizeOf.Card16 = sizeOf.USHORT;
encode.OffSize = encode.BYTE;
sizeOf.OffSize = sizeOf.BYTE;
encode.SID = encode.USHORT;
sizeOf.SID = sizeOf.USHORT;
encode.NUMBER = function(v) {
  if (v >= -107 && v <= 107) {
    return [v + 139];
  } else if (v >= 108 && v <= 1131) {
    v = v - 108;
    return [(v >> 8) + 247, v & 255];
  } else if (v >= -1131 && v <= -108) {
    v = -v - 108;
    return [(v >> 8) + 251, v & 255];
  } else if (v >= -32768 && v <= 32767) {
    return encode.NUMBER16(v);
  } else {
    return encode.NUMBER32(v);
  }
};
sizeOf.NUMBER = function(v) {
  return encode.NUMBER(v).length;
};
encode.NUMBER16 = function(v) {
  return [28, v >> 8 & 255, v & 255];
};
sizeOf.NUMBER16 = constant(3);
encode.NUMBER32 = function(v) {
  return [29, v >> 24 & 255, v >> 16 & 255, v >> 8 & 255, v & 255];
};
sizeOf.NUMBER32 = constant(5);
encode.REAL = function(v) {
  var value = v.toString();
  var m = /\.(\d*?)(?:9{5,20}|0{5,20})\d{0,2}(?:e(.+)|$)/.exec(value);
  if (m) {
    var epsilon = parseFloat("1e" + ((m[2] ? +m[2] : 0) + m[1].length));
    value = (Math.round(v * epsilon) / epsilon).toString();
  }
  var nibbles = "";
  for (var i = 0, ii = value.length;i < ii; i += 1) {
    var c = value[i];
    if (c === "e") {
      nibbles += value[++i] === "-" ? "c" : "b";
    } else if (c === ".") {
      nibbles += "a";
    } else if (c === "-") {
      nibbles += "e";
    } else {
      nibbles += c;
    }
  }
  nibbles += nibbles.length & 1 ? "f" : "ff";
  var out = [30];
  for (var i$1 = 0, ii$1 = nibbles.length;i$1 < ii$1; i$1 += 2) {
    out.push(parseInt(nibbles.substr(i$1, 2), 16));
  }
  return out;
};
sizeOf.REAL = function(v) {
  return encode.REAL(v).length;
};
encode.NAME = encode.CHARARRAY;
sizeOf.NAME = sizeOf.CHARARRAY;
encode.STRING = encode.CHARARRAY;
sizeOf.STRING = sizeOf.CHARARRAY;
decode.UTF8 = function(data, offset2, numBytes) {
  var codePoints = [];
  var numChars = numBytes;
  for (var j = 0;j < numChars; j++, offset2 += 1) {
    codePoints[j] = data.getUint8(offset2);
  }
  return String.fromCharCode.apply(null, codePoints);
};
decode.UTF16 = function(data, offset2, numBytes) {
  var codePoints = [];
  var numChars = numBytes / 2;
  for (var j = 0;j < numChars; j++, offset2 += 2) {
    codePoints[j] = data.getUint16(offset2);
  }
  return String.fromCharCode.apply(null, codePoints);
};
encode.UTF16 = function(v) {
  var b = [];
  for (var i = 0;i < v.length; i += 1) {
    var codepoint = v.charCodeAt(i);
    b[b.length] = codepoint >> 8 & 255;
    b[b.length] = codepoint & 255;
  }
  return b;
};
sizeOf.UTF16 = function(v) {
  return v.length * 2;
};
var eightBitMacEncodings = {
  "x-mac-croatian": "\xC4\xC5\xC7\xC9\xD1\xD6\xDC\xE1\xE0\xE2\xE4\xE3\xE5\xE7\xE9\xE8\xEA\xEB\xED\xEC\xEE\xEF\xF1\xF3\xF2\xF4\xF6\xF5\xFA\xF9\xFB\xFC\u2020\xB0\xA2\xA3\xA7\u2022\xB6\xDF\xAE\u0160\u2122\xB4\xA8\u2260\u017D\xD8\u221E\xB1\u2264\u2265\u2206\xB5\u2202\u2211\u220F\u0161\u222B\xAA\xBA\u03A9\u017E\xF8\xBF\xA1\xAC\u221A\u0192\u2248\u0106\xAB\u010C\u2026\xA0\xC0\xC3\xD5\u0152\u0153\u0110\u2014\u201C\u201D\u2018\u2019\xF7\u25CA\uF8FF\xA9\u2044\u20AC\u2039\u203A\xC6\xBB\u2013\xB7\u201A\u201E\u2030\xC2\u0107\xC1\u010D\xC8\xCD\xCE\xCF\xCC\xD3\xD4\u0111\xD2\xDA\xDB\xD9\u0131\u02C6\u02DC\xAF\u03C0\xCB\u02DA\xB8\xCA\xE6\u02C7",
  "x-mac-cyrillic": "\u0410\u0411\u0412\u0413\u0414\u0415\u0416\u0417\u0418\u0419\u041A\u041B\u041C\u041D\u041E\u041F\u0420\u0421\u0422\u0423\u0424\u0425\u0426\u0427\u0428\u0429\u042A\u042B\u042C\u042D\u042E\u042F\u2020\xB0\u0490\xA3\xA7\u2022\xB6\u0406\xAE\xA9\u2122\u0402\u0452\u2260\u0403\u0453\u221E\xB1\u2264\u2265\u0456\xB5\u0491\u0408\u0404\u0454\u0407\u0457\u0409\u0459\u040A\u045A\u0458\u0405\xAC\u221A\u0192\u2248\u2206\xAB\xBB\u2026\xA0\u040B\u045B\u040C\u045C\u0455\u2013\u2014\u201C\u201D\u2018\u2019\xF7\u201E\u040E\u045E\u040F\u045F\u2116\u0401\u0451\u044F\u0430\u0431\u0432\u0433\u0434\u0435\u0436\u0437\u0438\u0439\u043A\u043B\u043C\u043D\u043E\u043F\u0440\u0441\u0442\u0443\u0444\u0445\u0446\u0447\u0448\u0449\u044A\u044B\u044C\u044D\u044E",
  "x-mac-gaelic": "\xC4\xC5\xC7\xC9\xD1\xD6\xDC\xE1\xE0\xE2\xE4\xE3\xE5\xE7\xE9\xE8\xEA\xEB\xED\xEC\xEE\xEF\xF1\xF3\xF2\xF4\xF6\xF5\xFA\xF9\xFB\xFC\u2020\xB0\xA2\xA3\xA7\u2022\xB6\xDF\xAE\xA9\u2122\xB4\xA8\u2260\xC6\xD8\u1E02\xB1\u2264\u2265\u1E03\u010A\u010B\u1E0A\u1E0B\u1E1E\u1E1F\u0120\u0121\u1E40\xE6\xF8\u1E41\u1E56\u1E57\u027C\u0192\u017F\u1E60\xAB\xBB\u2026\xA0\xC0\xC3\xD5\u0152\u0153\u2013\u2014\u201C\u201D\u2018\u2019\u1E61\u1E9B\xFF\u0178\u1E6A\u20AC\u2039\u203A\u0176\u0177\u1E6B\xB7\u1EF2\u1EF3\u204A\xC2\xCA\xC1\xCB\xC8\xCD\xCE\xCF\xCC\xD3\xD4\u2663\xD2\xDA\xDB\xD9\u0131\xDD\xFD\u0174\u0175\u1E84\u1E85\u1E80\u1E81\u1E82\u1E83",
  "x-mac-greek": "\xC4\xB9\xB2\xC9\xB3\xD6\xDC\u0385\xE0\xE2\xE4\u0384\xA8\xE7\xE9\xE8\xEA\xEB\xA3\u2122\xEE\xEF\u2022\xBD\u2030\xF4\xF6\xA6\u20AC\xF9\xFB\xFC\u2020\u0393\u0394\u0398\u039B\u039E\u03A0\xDF\xAE\xA9\u03A3\u03AA\xA7\u2260\xB0\xB7\u0391\xB1\u2264\u2265\xA5\u0392\u0395\u0396\u0397\u0399\u039A\u039C\u03A6\u03AB\u03A8\u03A9\u03AC\u039D\xAC\u039F\u03A1\u2248\u03A4\xAB\xBB\u2026\xA0\u03A5\u03A7\u0386\u0388\u0153\u2013\u2015\u201C\u201D\u2018\u2019\xF7\u0389\u038A\u038C\u038E\u03AD\u03AE\u03AF\u03CC\u038F\u03CD\u03B1\u03B2\u03C8\u03B4\u03B5\u03C6\u03B3\u03B7\u03B9\u03BE\u03BA\u03BB\u03BC\u03BD\u03BF\u03C0\u03CE\u03C1\u03C3\u03C4\u03B8\u03C9\u03C2\u03C7\u03C5\u03B6\u03CA\u03CB\u0390\u03B0\xAD",
  "x-mac-icelandic": "\xC4\xC5\xC7\xC9\xD1\xD6\xDC\xE1\xE0\xE2\xE4\xE3\xE5\xE7\xE9\xE8\xEA\xEB\xED\xEC\xEE\xEF\xF1\xF3\xF2\xF4\xF6\xF5\xFA\xF9\xFB\xFC\xDD\xB0\xA2\xA3\xA7\u2022\xB6\xDF\xAE\xA9\u2122\xB4\xA8\u2260\xC6\xD8\u221E\xB1\u2264\u2265\xA5\xB5\u2202\u2211\u220F\u03C0\u222B\xAA\xBA\u03A9\xE6\xF8\xBF\xA1\xAC\u221A\u0192\u2248\u2206\xAB\xBB\u2026\xA0\xC0\xC3\xD5\u0152\u0153\u2013\u2014\u201C\u201D\u2018\u2019\xF7\u25CA\xFF\u0178\u2044\u20AC\xD0\xF0\xDE\xFE\xFD\xB7\u201A\u201E\u2030\xC2\xCA\xC1\xCB\xC8\xCD\xCE\xCF\xCC\xD3\xD4\uF8FF\xD2\xDA\xDB\xD9\u0131\u02C6\u02DC\xAF\u02D8\u02D9\u02DA\xB8\u02DD\u02DB\u02C7",
  "x-mac-inuit": "\u1403\u1404\u1405\u1406\u140A\u140B\u1431\u1432\u1433\u1434\u1438\u1439\u1449\u144E\u144F\u1450\u1451\u1455\u1456\u1466\u146D\u146E\u146F\u1470\u1472\u1473\u1483\u148B\u148C\u148D\u148E\u1490\u1491\xB0\u14A1\u14A5\u14A6\u2022\xB6\u14A7\xAE\xA9\u2122\u14A8\u14AA\u14AB\u14BB\u14C2\u14C3\u14C4\u14C5\u14C7\u14C8\u14D0\u14EF\u14F0\u14F1\u14F2\u14F4\u14F5\u1505\u14D5\u14D6\u14D7\u14D8\u14DA\u14DB\u14EA\u1528\u1529\u152A\u152B\u152D\u2026\xA0\u152E\u153E\u1555\u1556\u1557\u2013\u2014\u201C\u201D\u2018\u2019\u1558\u1559\u155A\u155D\u1546\u1547\u1548\u1549\u154B\u154C\u1550\u157F\u1580\u1581\u1582\u1583\u1584\u1585\u158F\u1590\u1591\u1592\u1593\u1594\u1595\u1671\u1672\u1673\u1674\u1675\u1676\u1596\u15A0\u15A1\u15A2\u15A3\u15A4\u15A5\u15A6\u157C\u0141\u0142",
  "x-mac-ce": "\xC4\u0100\u0101\xC9\u0104\xD6\xDC\xE1\u0105\u010C\xE4\u010D\u0106\u0107\xE9\u0179\u017A\u010E\xED\u010F\u0112\u0113\u0116\xF3\u0117\xF4\xF6\xF5\xFA\u011A\u011B\xFC\u2020\xB0\u0118\xA3\xA7\u2022\xB6\xDF\xAE\xA9\u2122\u0119\xA8\u2260\u0123\u012E\u012F\u012A\u2264\u2265\u012B\u0136\u2202\u2211\u0142\u013B\u013C\u013D\u013E\u0139\u013A\u0145\u0146\u0143\xAC\u221A\u0144\u0147\u2206\xAB\xBB\u2026\xA0\u0148\u0150\xD5\u0151\u014C\u2013\u2014\u201C\u201D\u2018\u2019\xF7\u25CA\u014D\u0154\u0155\u0158\u2039\u203A\u0159\u0156\u0157\u0160\u201A\u201E\u0161\u015A\u015B\xC1\u0164\u0165\xCD\u017D\u017E\u016A\xD3\xD4\u016B\u016E\xDA\u016F\u0170\u0171\u0172\u0173\xDD\xFD\u0137\u017B\u0141\u017C\u0122\u02C7",
  macintosh: "\xC4\xC5\xC7\xC9\xD1\xD6\xDC\xE1\xE0\xE2\xE4\xE3\xE5\xE7\xE9\xE8\xEA\xEB\xED\xEC\xEE\xEF\xF1\xF3\xF2\xF4\xF6\xF5\xFA\xF9\xFB\xFC\u2020\xB0\xA2\xA3\xA7\u2022\xB6\xDF\xAE\xA9\u2122\xB4\xA8\u2260\xC6\xD8\u221E\xB1\u2264\u2265\xA5\xB5\u2202\u2211\u220F\u03C0\u222B\xAA\xBA\u03A9\xE6\xF8\xBF\xA1\xAC\u221A\u0192\u2248\u2206\xAB\xBB\u2026\xA0\xC0\xC3\xD5\u0152\u0153\u2013\u2014\u201C\u201D\u2018\u2019\xF7\u25CA\xFF\u0178\u2044\u20AC\u2039\u203A\uFB01\uFB02\u2021\xB7\u201A\u201E\u2030\xC2\xCA\xC1\xCB\xC8\xCD\xCE\xCF\xCC\xD3\xD4\uF8FF\xD2\xDA\xDB\xD9\u0131\u02C6\u02DC\xAF\u02D8\u02D9\u02DA\xB8\u02DD\u02DB\u02C7",
  "x-mac-romanian": "\xC4\xC5\xC7\xC9\xD1\xD6\xDC\xE1\xE0\xE2\xE4\xE3\xE5\xE7\xE9\xE8\xEA\xEB\xED\xEC\xEE\xEF\xF1\xF3\xF2\xF4\xF6\xF5\xFA\xF9\xFB\xFC\u2020\xB0\xA2\xA3\xA7\u2022\xB6\xDF\xAE\xA9\u2122\xB4\xA8\u2260\u0102\u0218\u221E\xB1\u2264\u2265\xA5\xB5\u2202\u2211\u220F\u03C0\u222B\xAA\xBA\u03A9\u0103\u0219\xBF\xA1\xAC\u221A\u0192\u2248\u2206\xAB\xBB\u2026\xA0\xC0\xC3\xD5\u0152\u0153\u2013\u2014\u201C\u201D\u2018\u2019\xF7\u25CA\xFF\u0178\u2044\u20AC\u2039\u203A\u021A\u021B\u2021\xB7\u201A\u201E\u2030\xC2\xCA\xC1\xCB\xC8\xCD\xCE\xCF\xCC\xD3\xD4\uF8FF\xD2\xDA\xDB\xD9\u0131\u02C6\u02DC\xAF\u02D8\u02D9\u02DA\xB8\u02DD\u02DB\u02C7",
  "x-mac-turkish": "\xC4\xC5\xC7\xC9\xD1\xD6\xDC\xE1\xE0\xE2\xE4\xE3\xE5\xE7\xE9\xE8\xEA\xEB\xED\xEC\xEE\xEF\xF1\xF3\xF2\xF4\xF6\xF5\xFA\xF9\xFB\xFC\u2020\xB0\xA2\xA3\xA7\u2022\xB6\xDF\xAE\xA9\u2122\xB4\xA8\u2260\xC6\xD8\u221E\xB1\u2264\u2265\xA5\xB5\u2202\u2211\u220F\u03C0\u222B\xAA\xBA\u03A9\xE6\xF8\xBF\xA1\xAC\u221A\u0192\u2248\u2206\xAB\xBB\u2026\xA0\xC0\xC3\xD5\u0152\u0153\u2013\u2014\u201C\u201D\u2018\u2019\xF7\u25CA\xFF\u0178\u011E\u011F\u0130\u0131\u015E\u015F\u2021\xB7\u201A\u201E\u2030\xC2\xCA\xC1\xCB\xC8\xCD\xCE\xCF\xCC\xD3\xD4\uF8FF\xD2\xDA\xDB\xD9\uF8A0\u02C6\u02DC\xAF\u02D8\u02D9\u02DA\xB8\u02DD\u02DB\u02C7"
};
decode.MACSTRING = function(dataView, offset2, dataLength, encoding) {
  var table2 = eightBitMacEncodings[encoding];
  if (table2 === undefined) {
    return;
  }
  var result = "";
  for (var i = 0;i < dataLength; i++) {
    var c = dataView.getUint8(offset2 + i);
    if (c <= 127) {
      result += String.fromCharCode(c);
    } else {
      result += table2[c & 127];
    }
  }
  return result;
};
var macEncodingTableCache = typeof WeakMap === "function" && /* @__PURE__ */ new WeakMap;
var macEncodingCacheKeys;
var getMacEncodingTable = function(encoding) {
  if (!macEncodingCacheKeys) {
    macEncodingCacheKeys = {};
    for (var e in eightBitMacEncodings) {
      macEncodingCacheKeys[e] = new String(e);
    }
  }
  var cacheKey = macEncodingCacheKeys[encoding];
  if (cacheKey === undefined) {
    return;
  }
  if (macEncodingTableCache) {
    var cachedTable = macEncodingTableCache.get(cacheKey);
    if (cachedTable !== undefined) {
      return cachedTable;
    }
  }
  var decodingTable = eightBitMacEncodings[encoding];
  if (decodingTable === undefined) {
    return;
  }
  var encodingTable = {};
  for (var i = 0;i < decodingTable.length; i++) {
    encodingTable[decodingTable.charCodeAt(i)] = i + 128;
  }
  if (macEncodingTableCache) {
    macEncodingTableCache.set(cacheKey, encodingTable);
  }
  return encodingTable;
};
encode.MACSTRING = function(str, encoding) {
  var table2 = getMacEncodingTable(encoding);
  if (table2 === undefined) {
    return;
  }
  var result = [];
  for (var i = 0;i < str.length; i++) {
    var c = str.charCodeAt(i);
    if (c >= 128) {
      c = table2[c];
      if (c === undefined) {
        return;
      }
    }
    result[i] = c;
  }
  return result;
};
sizeOf.MACSTRING = function(str, encoding) {
  var b = encode.MACSTRING(str, encoding);
  if (b !== undefined) {
    return b.length;
  } else {
    return 0;
  }
};
function isByteEncodable(value) {
  return value >= -128 && value <= 127;
}
function encodeVarDeltaRunAsZeroes(deltas, pos, result) {
  var runLength = 0;
  var numDeltas = deltas.length;
  while (pos < numDeltas && runLength < 64 && deltas[pos] === 0) {
    ++pos;
    ++runLength;
  }
  result.push(128 | runLength - 1);
  return pos;
}
function encodeVarDeltaRunAsBytes(deltas, offset2, result) {
  var runLength = 0;
  var numDeltas = deltas.length;
  var pos = offset2;
  while (pos < numDeltas && runLength < 64) {
    var value = deltas[pos];
    if (!isByteEncodable(value)) {
      break;
    }
    if (value === 0 && pos + 1 < numDeltas && deltas[pos + 1] === 0) {
      break;
    }
    ++pos;
    ++runLength;
  }
  result.push(runLength - 1);
  for (var i = offset2;i < pos; ++i) {
    result.push(deltas[i] + 256 & 255);
  }
  return pos;
}
function encodeVarDeltaRunAsWords(deltas, offset2, result) {
  var runLength = 0;
  var numDeltas = deltas.length;
  var pos = offset2;
  while (pos < numDeltas && runLength < 64) {
    var value = deltas[pos];
    if (value === 0) {
      break;
    }
    if (isByteEncodable(value) && pos + 1 < numDeltas && isByteEncodable(deltas[pos + 1])) {
      break;
    }
    ++pos;
    ++runLength;
  }
  result.push(64 | runLength - 1);
  for (var i = offset2;i < pos; ++i) {
    var val = deltas[i];
    result.push(val + 65536 >> 8 & 255, val + 256 & 255);
  }
  return pos;
}
encode.VARDELTAS = function(deltas) {
  var pos = 0;
  var result = [];
  while (pos < deltas.length) {
    var value = deltas[pos];
    if (value === 0) {
      pos = encodeVarDeltaRunAsZeroes(deltas, pos, result);
    } else if (value >= -128 && value <= 127) {
      pos = encodeVarDeltaRunAsBytes(deltas, pos, result);
    } else {
      pos = encodeVarDeltaRunAsWords(deltas, pos, result);
    }
  }
  return result;
};
encode.INDEX = function(l) {
  var offset2 = 1;
  var offsets = [offset2];
  var data = [];
  for (var i = 0;i < l.length; i += 1) {
    var v = encode.OBJECT(l[i]);
    Array.prototype.push.apply(data, v);
    offset2 += v.length;
    offsets.push(offset2);
  }
  if (data.length === 0) {
    return [0, 0];
  }
  var encodedOffsets = [];
  var offSize = 1 + Math.floor(Math.log(offset2) / Math.log(2)) / 8 | 0;
  var offsetEncoder = [undefined, encode.BYTE, encode.USHORT, encode.UINT24, encode.ULONG][offSize];
  for (var i$1 = 0;i$1 < offsets.length; i$1 += 1) {
    var encodedOffset = offsetEncoder(offsets[i$1]);
    Array.prototype.push.apply(encodedOffsets, encodedOffset);
  }
  return Array.prototype.concat(encode.Card16(l.length), encode.OffSize(offSize), encodedOffsets, data);
};
sizeOf.INDEX = function(v) {
  return encode.INDEX(v).length;
};
encode.DICT = function(m) {
  var d = [];
  var keys = Object.keys(m);
  var length = keys.length;
  for (var i = 0;i < length; i += 1) {
    var k = parseInt(keys[i], 0);
    var v = m[k];
    d = d.concat(encode.OPERAND(v.value, v.type));
    d = d.concat(encode.OPERATOR(k));
  }
  return d;
};
sizeOf.DICT = function(m) {
  return encode.DICT(m).length;
};
encode.OPERATOR = function(v) {
  if (v < 1200) {
    return [v];
  } else {
    return [12, v - 1200];
  }
};
encode.OPERAND = function(v, type) {
  var d = [];
  if (Array.isArray(type)) {
    for (var i = 0;i < type.length; i += 1) {
      check.argument(v.length === type.length, "Not enough arguments given for type" + type);
      d = d.concat(encode.OPERAND(v[i], type[i]));
    }
  } else {
    if (type === "SID") {
      d = d.concat(encode.NUMBER(v));
    } else if (type === "offset") {
      d = d.concat(encode.NUMBER32(v));
    } else if (type === "number") {
      d = d.concat(encode.NUMBER(v));
    } else if (type === "real") {
      d = d.concat(encode.REAL(v));
    } else {
      throw new Error("Unknown operand type " + type);
    }
  }
  return d;
};
encode.OP = encode.BYTE;
sizeOf.OP = sizeOf.BYTE;
var wmm = typeof WeakMap === "function" && /* @__PURE__ */ new WeakMap;
encode.CHARSTRING = function(ops) {
  if (wmm) {
    var cachedValue = wmm.get(ops);
    if (cachedValue !== undefined) {
      return cachedValue;
    }
  }
  var d = [];
  var length = ops.length;
  for (var i = 0;i < length; i += 1) {
    var op = ops[i];
    d = d.concat(encode[op.type](op.value));
  }
  if (wmm) {
    wmm.set(ops, d);
  }
  return d;
};
sizeOf.CHARSTRING = function(ops) {
  return encode.CHARSTRING(ops).length;
};
encode.OBJECT = function(v) {
  var encodingFunction = encode[v.type];
  check.argument(encodingFunction !== undefined, "No encoding function for type " + v.type);
  return encodingFunction(v.value);
};
sizeOf.OBJECT = function(v) {
  var sizeOfFunction = sizeOf[v.type];
  check.argument(sizeOfFunction !== undefined, "No sizeOf function for type " + v.type);
  return sizeOfFunction(v.value);
};
encode.TABLE = function(table2) {
  var d = [];
  var length = table2.fields.length;
  var subtables = [];
  var subtableOffsets = [];
  for (var i = 0;i < length; i += 1) {
    var field = table2.fields[i];
    var encodingFunction = encode[field.type];
    check.argument(encodingFunction !== undefined, "No encoding function for field type " + field.type + " (" + field.name + ")");
    var value = table2[field.name];
    if (value === undefined) {
      value = field.value;
    }
    var bytes = encodingFunction(value);
    if (field.type === "TABLE") {
      subtableOffsets.push(d.length);
      d = d.concat([0, 0]);
      subtables.push(bytes);
    } else {
      d = d.concat(bytes);
    }
  }
  for (var i$1 = 0;i$1 < subtables.length; i$1 += 1) {
    var o = subtableOffsets[i$1];
    var offset2 = d.length;
    check.argument(offset2 < 65536, "Table " + table2.tableName + " too big.");
    d[o] = offset2 >> 8;
    d[o + 1] = offset2 & 255;
    d = d.concat(subtables[i$1]);
  }
  return d;
};
sizeOf.TABLE = function(table2) {
  var numBytes = 0;
  var length = table2.fields.length;
  for (var i = 0;i < length; i += 1) {
    var field = table2.fields[i];
    var sizeOfFunction = sizeOf[field.type];
    check.argument(sizeOfFunction !== undefined, "No sizeOf function for field type " + field.type + " (" + field.name + ")");
    var value = table2[field.name];
    if (value === undefined) {
      value = field.value;
    }
    numBytes += sizeOfFunction(value);
    if (field.type === "TABLE") {
      numBytes += 2;
    }
  }
  return numBytes;
};
encode.RECORD = encode.TABLE;
sizeOf.RECORD = sizeOf.TABLE;
encode.LITERAL = function(v) {
  return v;
};
sizeOf.LITERAL = function(v) {
  return v.length;
};
function Table(tableName, fields, options) {
  if (fields.length && (fields[0].name !== "coverageFormat" || fields[0].value === 1)) {
    for (var i = 0;i < fields.length; i += 1) {
      var field = fields[i];
      this[field.name] = field.value;
    }
  }
  this.tableName = tableName;
  this.fields = fields;
  if (options) {
    var optionKeys = Object.keys(options);
    for (var i$1 = 0;i$1 < optionKeys.length; i$1 += 1) {
      var k = optionKeys[i$1];
      var v = options[k];
      if (this[k] !== undefined) {
        this[k] = v;
      }
    }
  }
}
Table.prototype.encode = function() {
  return encode.TABLE(this);
};
Table.prototype.sizeOf = function() {
  return sizeOf.TABLE(this);
};
function ushortList(itemName, list, count) {
  if (count === undefined) {
    count = list.length;
  }
  var fields = new Array(list.length + 1);
  fields[0] = { name: itemName + "Count", type: "USHORT", value: count };
  for (var i = 0;i < list.length; i++) {
    fields[i + 1] = { name: itemName + i, type: "USHORT", value: list[i] };
  }
  return fields;
}
function tableList(itemName, records, itemCallback) {
  var count = records.length;
  var fields = new Array(count + 1);
  fields[0] = { name: itemName + "Count", type: "USHORT", value: count };
  for (var i = 0;i < count; i++) {
    fields[i + 1] = { name: itemName + i, type: "TABLE", value: itemCallback(records[i], i) };
  }
  return fields;
}
function recordList(itemName, records, itemCallback) {
  var count = records.length;
  var fields = [];
  fields[0] = { name: itemName + "Count", type: "USHORT", value: count };
  for (var i = 0;i < count; i++) {
    fields = fields.concat(itemCallback(records[i], i));
  }
  return fields;
}
function Coverage(coverageTable) {
  if (coverageTable.format === 1) {
    Table.call(this, "coverageTable", [{ name: "coverageFormat", type: "USHORT", value: 1 }].concat(ushortList("glyph", coverageTable.glyphs)));
  } else if (coverageTable.format === 2) {
    Table.call(this, "coverageTable", [{ name: "coverageFormat", type: "USHORT", value: 2 }].concat(recordList("rangeRecord", coverageTable.ranges, function(RangeRecord) {
      return [
        { name: "startGlyphID", type: "USHORT", value: RangeRecord.start },
        { name: "endGlyphID", type: "USHORT", value: RangeRecord.end },
        { name: "startCoverageIndex", type: "USHORT", value: RangeRecord.index }
      ];
    })));
  } else {
    check.assert(false, "Coverage format must be 1 or 2.");
  }
}
Coverage.prototype = Object.create(Table.prototype);
Coverage.prototype.constructor = Coverage;
function ScriptList(scriptListTable) {
  Table.call(this, "scriptListTable", recordList("scriptRecord", scriptListTable, function(scriptRecord, i) {
    var script = scriptRecord.script;
    var defaultLangSys = script.defaultLangSys;
    check.assert(!!defaultLangSys, "Unable to write GSUB: script " + scriptRecord.tag + " has no default language system.");
    return [
      { name: "scriptTag" + i, type: "TAG", value: scriptRecord.tag },
      { name: "script" + i, type: "TABLE", value: new Table("scriptTable", [
        { name: "defaultLangSys", type: "TABLE", value: new Table("defaultLangSys", [
          { name: "lookupOrder", type: "USHORT", value: 0 },
          { name: "reqFeatureIndex", type: "USHORT", value: defaultLangSys.reqFeatureIndex }
        ].concat(ushortList("featureIndex", defaultLangSys.featureIndexes))) }
      ].concat(recordList("langSys", script.langSysRecords, function(langSysRecord, i2) {
        var langSys = langSysRecord.langSys;
        return [
          { name: "langSysTag" + i2, type: "TAG", value: langSysRecord.tag },
          { name: "langSys" + i2, type: "TABLE", value: new Table("langSys", [
            { name: "lookupOrder", type: "USHORT", value: 0 },
            { name: "reqFeatureIndex", type: "USHORT", value: langSys.reqFeatureIndex }
          ].concat(ushortList("featureIndex", langSys.featureIndexes))) }
        ];
      }))) }
    ];
  }));
}
ScriptList.prototype = Object.create(Table.prototype);
ScriptList.prototype.constructor = ScriptList;
function FeatureList(featureListTable) {
  Table.call(this, "featureListTable", recordList("featureRecord", featureListTable, function(featureRecord, i) {
    var feature = featureRecord.feature;
    return [
      { name: "featureTag" + i, type: "TAG", value: featureRecord.tag },
      { name: "feature" + i, type: "TABLE", value: new Table("featureTable", [
        { name: "featureParams", type: "USHORT", value: feature.featureParams }
      ].concat(ushortList("lookupListIndex", feature.lookupListIndexes))) }
    ];
  }));
}
FeatureList.prototype = Object.create(Table.prototype);
FeatureList.prototype.constructor = FeatureList;
function LookupList(lookupListTable, subtableMakers2) {
  Table.call(this, "lookupListTable", tableList("lookup", lookupListTable, function(lookupTable) {
    var subtableCallback = subtableMakers2[lookupTable.lookupType];
    check.assert(!!subtableCallback, "Unable to write GSUB lookup type " + lookupTable.lookupType + " tables.");
    return new Table("lookupTable", [
      { name: "lookupType", type: "USHORT", value: lookupTable.lookupType },
      { name: "lookupFlag", type: "USHORT", value: lookupTable.lookupFlag }
    ].concat(tableList("subtable", lookupTable.subtables, subtableCallback)));
  }));
}
LookupList.prototype = Object.create(Table.prototype);
LookupList.prototype.constructor = LookupList;
var table = {
  Table,
  Record: Table,
  Coverage,
  ScriptList,
  FeatureList,
  LookupList,
  ushortList,
  tableList,
  recordList
};
function getByte(dataView, offset2) {
  return dataView.getUint8(offset2);
}
function getUShort(dataView, offset2) {
  return dataView.getUint16(offset2, false);
}
function getShort(dataView, offset2) {
  return dataView.getInt16(offset2, false);
}
function getULong(dataView, offset2) {
  return dataView.getUint32(offset2, false);
}
function getFixed(dataView, offset2) {
  var decimal = dataView.getInt16(offset2, false);
  var fraction = dataView.getUint16(offset2 + 2, false);
  return decimal + fraction / 65535;
}
function getTag(dataView, offset2) {
  var tag = "";
  for (var i = offset2;i < offset2 + 4; i += 1) {
    tag += String.fromCharCode(dataView.getInt8(i));
  }
  return tag;
}
function getOffset(dataView, offset2, offSize) {
  var v = 0;
  for (var i = 0;i < offSize; i += 1) {
    v <<= 8;
    v += dataView.getUint8(offset2 + i);
  }
  return v;
}
function getBytes(dataView, startOffset, endOffset) {
  var bytes = [];
  for (var i = startOffset;i < endOffset; i += 1) {
    bytes.push(dataView.getUint8(i));
  }
  return bytes;
}
function bytesToString(bytes) {
  var s = "";
  for (var i = 0;i < bytes.length; i += 1) {
    s += String.fromCharCode(bytes[i]);
  }
  return s;
}
var typeOffsets = {
  byte: 1,
  uShort: 2,
  short: 2,
  uLong: 4,
  fixed: 4,
  longDateTime: 8,
  tag: 4
};
function Parser(data, offset2) {
  this.data = data;
  this.offset = offset2;
  this.relativeOffset = 0;
}
Parser.prototype.parseByte = function() {
  var v = this.data.getUint8(this.offset + this.relativeOffset);
  this.relativeOffset += 1;
  return v;
};
Parser.prototype.parseChar = function() {
  var v = this.data.getInt8(this.offset + this.relativeOffset);
  this.relativeOffset += 1;
  return v;
};
Parser.prototype.parseCard8 = Parser.prototype.parseByte;
Parser.prototype.parseUShort = function() {
  var v = this.data.getUint16(this.offset + this.relativeOffset);
  this.relativeOffset += 2;
  return v;
};
Parser.prototype.parseCard16 = Parser.prototype.parseUShort;
Parser.prototype.parseSID = Parser.prototype.parseUShort;
Parser.prototype.parseOffset16 = Parser.prototype.parseUShort;
Parser.prototype.parseShort = function() {
  var v = this.data.getInt16(this.offset + this.relativeOffset);
  this.relativeOffset += 2;
  return v;
};
Parser.prototype.parseF2Dot14 = function() {
  var v = this.data.getInt16(this.offset + this.relativeOffset) / 16384;
  this.relativeOffset += 2;
  return v;
};
Parser.prototype.parseULong = function() {
  var v = getULong(this.data, this.offset + this.relativeOffset);
  this.relativeOffset += 4;
  return v;
};
Parser.prototype.parseOffset32 = Parser.prototype.parseULong;
Parser.prototype.parseFixed = function() {
  var v = getFixed(this.data, this.offset + this.relativeOffset);
  this.relativeOffset += 4;
  return v;
};
Parser.prototype.parseString = function(length) {
  var dataView = this.data;
  var offset2 = this.offset + this.relativeOffset;
  var string = "";
  this.relativeOffset += length;
  for (var i = 0;i < length; i++) {
    string += String.fromCharCode(dataView.getUint8(offset2 + i));
  }
  return string;
};
Parser.prototype.parseTag = function() {
  return this.parseString(4);
};
Parser.prototype.parseLongDateTime = function() {
  var v = getULong(this.data, this.offset + this.relativeOffset + 4);
  v -= 2082844800;
  this.relativeOffset += 8;
  return v;
};
Parser.prototype.parseVersion = function(minorBase) {
  var major = getUShort(this.data, this.offset + this.relativeOffset);
  var minor = getUShort(this.data, this.offset + this.relativeOffset + 2);
  this.relativeOffset += 4;
  if (minorBase === undefined) {
    minorBase = 4096;
  }
  return major + minor / minorBase / 10;
};
Parser.prototype.skip = function(type, amount) {
  if (amount === undefined) {
    amount = 1;
  }
  this.relativeOffset += typeOffsets[type] * amount;
};
Parser.prototype.parseULongList = function(count) {
  if (count === undefined) {
    count = this.parseULong();
  }
  var offsets = new Array(count);
  var dataView = this.data;
  var offset2 = this.offset + this.relativeOffset;
  for (var i = 0;i < count; i++) {
    offsets[i] = dataView.getUint32(offset2);
    offset2 += 4;
  }
  this.relativeOffset += count * 4;
  return offsets;
};
Parser.prototype.parseOffset16List = Parser.prototype.parseUShortList = function(count) {
  if (count === undefined) {
    count = this.parseUShort();
  }
  var offsets = new Array(count);
  var dataView = this.data;
  var offset2 = this.offset + this.relativeOffset;
  for (var i = 0;i < count; i++) {
    offsets[i] = dataView.getUint16(offset2);
    offset2 += 2;
  }
  this.relativeOffset += count * 2;
  return offsets;
};
Parser.prototype.parseShortList = function(count) {
  var list = new Array(count);
  var dataView = this.data;
  var offset2 = this.offset + this.relativeOffset;
  for (var i = 0;i < count; i++) {
    list[i] = dataView.getInt16(offset2);
    offset2 += 2;
  }
  this.relativeOffset += count * 2;
  return list;
};
Parser.prototype.parseByteList = function(count) {
  var list = new Array(count);
  var dataView = this.data;
  var offset2 = this.offset + this.relativeOffset;
  for (var i = 0;i < count; i++) {
    list[i] = dataView.getUint8(offset2++);
  }
  this.relativeOffset += count;
  return list;
};
Parser.prototype.parseList = function(count, itemCallback) {
  if (!itemCallback) {
    itemCallback = count;
    count = this.parseUShort();
  }
  var list = new Array(count);
  for (var i = 0;i < count; i++) {
    list[i] = itemCallback.call(this);
  }
  return list;
};
Parser.prototype.parseList32 = function(count, itemCallback) {
  if (!itemCallback) {
    itemCallback = count;
    count = this.parseULong();
  }
  var list = new Array(count);
  for (var i = 0;i < count; i++) {
    list[i] = itemCallback.call(this);
  }
  return list;
};
Parser.prototype.parseRecordList = function(count, recordDescription) {
  if (!recordDescription) {
    recordDescription = count;
    count = this.parseUShort();
  }
  var records = new Array(count);
  var fields = Object.keys(recordDescription);
  for (var i = 0;i < count; i++) {
    var rec = {};
    for (var j = 0;j < fields.length; j++) {
      var fieldName = fields[j];
      var fieldType = recordDescription[fieldName];
      rec[fieldName] = fieldType.call(this);
    }
    records[i] = rec;
  }
  return records;
};
Parser.prototype.parseRecordList32 = function(count, recordDescription) {
  if (!recordDescription) {
    recordDescription = count;
    count = this.parseULong();
  }
  var records = new Array(count);
  var fields = Object.keys(recordDescription);
  for (var i = 0;i < count; i++) {
    var rec = {};
    for (var j = 0;j < fields.length; j++) {
      var fieldName = fields[j];
      var fieldType = recordDescription[fieldName];
      rec[fieldName] = fieldType.call(this);
    }
    records[i] = rec;
  }
  return records;
};
Parser.prototype.parseStruct = function(description) {
  if (typeof description === "function") {
    return description.call(this);
  } else {
    var fields = Object.keys(description);
    var struct = {};
    for (var j = 0;j < fields.length; j++) {
      var fieldName = fields[j];
      var fieldType = description[fieldName];
      struct[fieldName] = fieldType.call(this);
    }
    return struct;
  }
};
Parser.prototype.parseValueRecord = function(valueFormat) {
  if (valueFormat === undefined) {
    valueFormat = this.parseUShort();
  }
  if (valueFormat === 0) {
    return;
  }
  var valueRecord = {};
  if (valueFormat & 1) {
    valueRecord.xPlacement = this.parseShort();
  }
  if (valueFormat & 2) {
    valueRecord.yPlacement = this.parseShort();
  }
  if (valueFormat & 4) {
    valueRecord.xAdvance = this.parseShort();
  }
  if (valueFormat & 8) {
    valueRecord.yAdvance = this.parseShort();
  }
  if (valueFormat & 16) {
    valueRecord.xPlaDevice = undefined;
    this.parseShort();
  }
  if (valueFormat & 32) {
    valueRecord.yPlaDevice = undefined;
    this.parseShort();
  }
  if (valueFormat & 64) {
    valueRecord.xAdvDevice = undefined;
    this.parseShort();
  }
  if (valueFormat & 128) {
    valueRecord.yAdvDevice = undefined;
    this.parseShort();
  }
  return valueRecord;
};
Parser.prototype.parseValueRecordList = function() {
  var valueFormat = this.parseUShort();
  var valueCount = this.parseUShort();
  var values = new Array(valueCount);
  for (var i = 0;i < valueCount; i++) {
    values[i] = this.parseValueRecord(valueFormat);
  }
  return values;
};
Parser.prototype.parsePointer = function(description) {
  var structOffset = this.parseOffset16();
  if (structOffset > 0) {
    return new Parser(this.data, this.offset + structOffset).parseStruct(description);
  }
  return;
};
Parser.prototype.parsePointer32 = function(description) {
  var structOffset = this.parseOffset32();
  if (structOffset > 0) {
    return new Parser(this.data, this.offset + structOffset).parseStruct(description);
  }
  return;
};
Parser.prototype.parseListOfLists = function(itemCallback) {
  var offsets = this.parseOffset16List();
  var count = offsets.length;
  var relativeOffset = this.relativeOffset;
  var list = new Array(count);
  for (var i = 0;i < count; i++) {
    var start = offsets[i];
    if (start === 0) {
      list[i] = undefined;
      continue;
    }
    this.relativeOffset = start;
    if (itemCallback) {
      var subOffsets = this.parseOffset16List();
      var subList = new Array(subOffsets.length);
      for (var j = 0;j < subOffsets.length; j++) {
        this.relativeOffset = start + subOffsets[j];
        subList[j] = itemCallback.call(this);
      }
      list[i] = subList;
    } else {
      list[i] = this.parseUShortList();
    }
  }
  this.relativeOffset = relativeOffset;
  return list;
};
Parser.prototype.parseCoverage = function() {
  var startOffset = this.offset + this.relativeOffset;
  var format = this.parseUShort();
  var count = this.parseUShort();
  if (format === 1) {
    return {
      format: 1,
      glyphs: this.parseUShortList(count)
    };
  } else if (format === 2) {
    var ranges = new Array(count);
    for (var i = 0;i < count; i++) {
      ranges[i] = {
        start: this.parseUShort(),
        end: this.parseUShort(),
        index: this.parseUShort()
      };
    }
    return {
      format: 2,
      ranges
    };
  }
  throw new Error("0x" + startOffset.toString(16) + ": Coverage format must be 1 or 2.");
};
Parser.prototype.parseClassDef = function() {
  var startOffset = this.offset + this.relativeOffset;
  var format = this.parseUShort();
  if (format === 1) {
    return {
      format: 1,
      startGlyph: this.parseUShort(),
      classes: this.parseUShortList()
    };
  } else if (format === 2) {
    return {
      format: 2,
      ranges: this.parseRecordList({
        start: Parser.uShort,
        end: Parser.uShort,
        classId: Parser.uShort
      })
    };
  }
  throw new Error("0x" + startOffset.toString(16) + ": ClassDef format must be 1 or 2.");
};
Parser.list = function(count, itemCallback) {
  return function() {
    return this.parseList(count, itemCallback);
  };
};
Parser.list32 = function(count, itemCallback) {
  return function() {
    return this.parseList32(count, itemCallback);
  };
};
Parser.recordList = function(count, recordDescription) {
  return function() {
    return this.parseRecordList(count, recordDescription);
  };
};
Parser.recordList32 = function(count, recordDescription) {
  return function() {
    return this.parseRecordList32(count, recordDescription);
  };
};
Parser.pointer = function(description) {
  return function() {
    return this.parsePointer(description);
  };
};
Parser.pointer32 = function(description) {
  return function() {
    return this.parsePointer32(description);
  };
};
Parser.tag = Parser.prototype.parseTag;
Parser.byte = Parser.prototype.parseByte;
Parser.uShort = Parser.offset16 = Parser.prototype.parseUShort;
Parser.uShortList = Parser.prototype.parseUShortList;
Parser.uLong = Parser.offset32 = Parser.prototype.parseULong;
Parser.uLongList = Parser.prototype.parseULongList;
Parser.struct = Parser.prototype.parseStruct;
Parser.coverage = Parser.prototype.parseCoverage;
Parser.classDef = Parser.prototype.parseClassDef;
var langSysTable = {
  reserved: Parser.uShort,
  reqFeatureIndex: Parser.uShort,
  featureIndexes: Parser.uShortList
};
Parser.prototype.parseScriptList = function() {
  return this.parsePointer(Parser.recordList({
    tag: Parser.tag,
    script: Parser.pointer({
      defaultLangSys: Parser.pointer(langSysTable),
      langSysRecords: Parser.recordList({
        tag: Parser.tag,
        langSys: Parser.pointer(langSysTable)
      })
    })
  })) || [];
};
Parser.prototype.parseFeatureList = function() {
  return this.parsePointer(Parser.recordList({
    tag: Parser.tag,
    feature: Parser.pointer({
      featureParams: Parser.offset16,
      lookupListIndexes: Parser.uShortList
    })
  })) || [];
};
Parser.prototype.parseLookupList = function(lookupTableParsers) {
  return this.parsePointer(Parser.list(Parser.pointer(function() {
    var lookupType = this.parseUShort();
    check.argument(1 <= lookupType && lookupType <= 9, "GPOS/GSUB lookup type " + lookupType + " unknown.");
    var lookupFlag = this.parseUShort();
    var useMarkFilteringSet = lookupFlag & 16;
    return {
      lookupType,
      lookupFlag,
      subtables: this.parseList(Parser.pointer(lookupTableParsers[lookupType])),
      markFilteringSet: useMarkFilteringSet ? this.parseUShort() : undefined
    };
  }))) || [];
};
Parser.prototype.parseFeatureVariationsList = function() {
  return this.parsePointer32(function() {
    var majorVersion = this.parseUShort();
    var minorVersion = this.parseUShort();
    check.argument(majorVersion === 1 && minorVersion < 1, "GPOS/GSUB feature variations table unknown.");
    var featureVariations = this.parseRecordList32({
      conditionSetOffset: Parser.offset32,
      featureTableSubstitutionOffset: Parser.offset32
    });
    return featureVariations;
  }) || [];
};
var parse = {
  getByte,
  getCard8: getByte,
  getUShort,
  getCard16: getUShort,
  getShort,
  getULong,
  getFixed,
  getTag,
  getOffset,
  getBytes,
  bytesToString,
  Parser
};
function parseCmapTableFormat12(cmap2, p) {
  p.parseUShort();
  cmap2.length = p.parseULong();
  cmap2.language = p.parseULong();
  var groupCount;
  cmap2.groupCount = groupCount = p.parseULong();
  cmap2.glyphIndexMap = {};
  for (var i = 0;i < groupCount; i += 1) {
    var startCharCode = p.parseULong();
    var endCharCode = p.parseULong();
    var startGlyphId = p.parseULong();
    for (var c = startCharCode;c <= endCharCode; c += 1) {
      cmap2.glyphIndexMap[c] = startGlyphId;
      startGlyphId++;
    }
  }
}
function parseCmapTableFormat4(cmap2, p, data, start, offset2) {
  cmap2.length = p.parseUShort();
  cmap2.language = p.parseUShort();
  var segCount;
  cmap2.segCount = segCount = p.parseUShort() >> 1;
  p.skip("uShort", 3);
  cmap2.glyphIndexMap = {};
  var endCountParser = new parse.Parser(data, start + offset2 + 14);
  var startCountParser = new parse.Parser(data, start + offset2 + 16 + segCount * 2);
  var idDeltaParser = new parse.Parser(data, start + offset2 + 16 + segCount * 4);
  var idRangeOffsetParser = new parse.Parser(data, start + offset2 + 16 + segCount * 6);
  var glyphIndexOffset = start + offset2 + 16 + segCount * 8;
  for (var i = 0;i < segCount - 1; i += 1) {
    var glyphIndex = undefined;
    var endCount = endCountParser.parseUShort();
    var startCount = startCountParser.parseUShort();
    var idDelta = idDeltaParser.parseShort();
    var idRangeOffset = idRangeOffsetParser.parseUShort();
    for (var c = startCount;c <= endCount; c += 1) {
      if (idRangeOffset !== 0) {
        glyphIndexOffset = idRangeOffsetParser.offset + idRangeOffsetParser.relativeOffset - 2;
        glyphIndexOffset += idRangeOffset;
        glyphIndexOffset += (c - startCount) * 2;
        glyphIndex = parse.getUShort(data, glyphIndexOffset);
        if (glyphIndex !== 0) {
          glyphIndex = glyphIndex + idDelta & 65535;
        }
      } else {
        glyphIndex = c + idDelta & 65535;
      }
      cmap2.glyphIndexMap[c] = glyphIndex;
    }
  }
}
function parseCmapTable(data, start) {
  var cmap2 = {};
  cmap2.version = parse.getUShort(data, start);
  check.argument(cmap2.version === 0, "cmap table version should be 0.");
  cmap2.numTables = parse.getUShort(data, start + 2);
  var offset2 = -1;
  for (var i = cmap2.numTables - 1;i >= 0; i -= 1) {
    var platformId = parse.getUShort(data, start + 4 + i * 8);
    var encodingId = parse.getUShort(data, start + 4 + i * 8 + 2);
    if (platformId === 3 && (encodingId === 0 || encodingId === 1 || encodingId === 10) || platformId === 0 && (encodingId === 0 || encodingId === 1 || encodingId === 2 || encodingId === 3 || encodingId === 4)) {
      offset2 = parse.getULong(data, start + 4 + i * 8 + 4);
      break;
    }
  }
  if (offset2 === -1) {
    throw new Error("No valid cmap sub-tables found.");
  }
  var p = new parse.Parser(data, start + offset2);
  cmap2.format = p.parseUShort();
  if (cmap2.format === 12) {
    parseCmapTableFormat12(cmap2, p);
  } else if (cmap2.format === 4) {
    parseCmapTableFormat4(cmap2, p, data, start, offset2);
  } else {
    throw new Error("Only format 4 and 12 cmap tables are supported (found format " + cmap2.format + ").");
  }
  return cmap2;
}
function addSegment(t, code, glyphIndex) {
  t.segments.push({
    end: code,
    start: code,
    delta: -(code - glyphIndex),
    offset: 0,
    glyphIndex
  });
}
function addTerminatorSegment(t) {
  t.segments.push({
    end: 65535,
    start: 65535,
    delta: 1,
    offset: 0
  });
}
function makeCmapTable(glyphs) {
  var isPlan0Only = true;
  var i;
  for (i = glyphs.length - 1;i > 0; i -= 1) {
    var g = glyphs.get(i);
    if (g.unicode > 65535) {
      console.log("Adding CMAP format 12 (needed!)");
      isPlan0Only = false;
      break;
    }
  }
  var cmapTable = [
    { name: "version", type: "USHORT", value: 0 },
    { name: "numTables", type: "USHORT", value: isPlan0Only ? 1 : 2 },
    { name: "platformID", type: "USHORT", value: 3 },
    { name: "encodingID", type: "USHORT", value: 1 },
    { name: "offset", type: "ULONG", value: isPlan0Only ? 12 : 12 + 8 }
  ];
  if (!isPlan0Only) {
    cmapTable = cmapTable.concat([
      { name: "cmap12PlatformID", type: "USHORT", value: 3 },
      { name: "cmap12EncodingID", type: "USHORT", value: 10 },
      { name: "cmap12Offset", type: "ULONG", value: 0 }
    ]);
  }
  cmapTable = cmapTable.concat([
    { name: "format", type: "USHORT", value: 4 },
    { name: "cmap4Length", type: "USHORT", value: 0 },
    { name: "language", type: "USHORT", value: 0 },
    { name: "segCountX2", type: "USHORT", value: 0 },
    { name: "searchRange", type: "USHORT", value: 0 },
    { name: "entrySelector", type: "USHORT", value: 0 },
    { name: "rangeShift", type: "USHORT", value: 0 }
  ]);
  var t = new table.Table("cmap", cmapTable);
  t.segments = [];
  for (i = 0;i < glyphs.length; i += 1) {
    var glyph = glyphs.get(i);
    for (var j = 0;j < glyph.unicodes.length; j += 1) {
      addSegment(t, glyph.unicodes[j], i);
    }
    t.segments = t.segments.sort(function(a, b) {
      return a.start - b.start;
    });
  }
  addTerminatorSegment(t);
  var segCount = t.segments.length;
  var segCountToRemove = 0;
  var endCounts = [];
  var startCounts = [];
  var idDeltas = [];
  var idRangeOffsets = [];
  var glyphIds = [];
  var cmap12Groups = [];
  for (i = 0;i < segCount; i += 1) {
    var segment = t.segments[i];
    if (segment.end <= 65535 && segment.start <= 65535) {
      endCounts = endCounts.concat({ name: "end_" + i, type: "USHORT", value: segment.end });
      startCounts = startCounts.concat({ name: "start_" + i, type: "USHORT", value: segment.start });
      idDeltas = idDeltas.concat({ name: "idDelta_" + i, type: "SHORT", value: segment.delta });
      idRangeOffsets = idRangeOffsets.concat({ name: "idRangeOffset_" + i, type: "USHORT", value: segment.offset });
      if (segment.glyphId !== undefined) {
        glyphIds = glyphIds.concat({ name: "glyph_" + i, type: "USHORT", value: segment.glyphId });
      }
    } else {
      segCountToRemove += 1;
    }
    if (!isPlan0Only && segment.glyphIndex !== undefined) {
      cmap12Groups = cmap12Groups.concat({ name: "cmap12Start_" + i, type: "ULONG", value: segment.start });
      cmap12Groups = cmap12Groups.concat({ name: "cmap12End_" + i, type: "ULONG", value: segment.end });
      cmap12Groups = cmap12Groups.concat({ name: "cmap12Glyph_" + i, type: "ULONG", value: segment.glyphIndex });
    }
  }
  t.segCountX2 = (segCount - segCountToRemove) * 2;
  t.searchRange = Math.pow(2, Math.floor(Math.log(segCount - segCountToRemove) / Math.log(2))) * 2;
  t.entrySelector = Math.log(t.searchRange / 2) / Math.log(2);
  t.rangeShift = t.segCountX2 - t.searchRange;
  t.fields = t.fields.concat(endCounts);
  t.fields.push({ name: "reservedPad", type: "USHORT", value: 0 });
  t.fields = t.fields.concat(startCounts);
  t.fields = t.fields.concat(idDeltas);
  t.fields = t.fields.concat(idRangeOffsets);
  t.fields = t.fields.concat(glyphIds);
  t.cmap4Length = 14 + endCounts.length * 2 + 2 + startCounts.length * 2 + idDeltas.length * 2 + idRangeOffsets.length * 2 + glyphIds.length * 2;
  if (!isPlan0Only) {
    var cmap12Length = 16 + cmap12Groups.length * 4;
    t.cmap12Offset = 12 + 2 * 2 + 4 + t.cmap4Length;
    t.fields = t.fields.concat([
      { name: "cmap12Format", type: "USHORT", value: 12 },
      { name: "cmap12Reserved", type: "USHORT", value: 0 },
      { name: "cmap12Length", type: "ULONG", value: cmap12Length },
      { name: "cmap12Language", type: "ULONG", value: 0 },
      { name: "cmap12nGroups", type: "ULONG", value: cmap12Groups.length / 3 }
    ]);
    t.fields = t.fields.concat(cmap12Groups);
  }
  return t;
}
var cmap = { parse: parseCmapTable, make: makeCmapTable };
var cffStandardStrings = [
  ".notdef",
  "space",
  "exclam",
  "quotedbl",
  "numbersign",
  "dollar",
  "percent",
  "ampersand",
  "quoteright",
  "parenleft",
  "parenright",
  "asterisk",
  "plus",
  "comma",
  "hyphen",
  "period",
  "slash",
  "zero",
  "one",
  "two",
  "three",
  "four",
  "five",
  "six",
  "seven",
  "eight",
  "nine",
  "colon",
  "semicolon",
  "less",
  "equal",
  "greater",
  "question",
  "at",
  "A",
  "B",
  "C",
  "D",
  "E",
  "F",
  "G",
  "H",
  "I",
  "J",
  "K",
  "L",
  "M",
  "N",
  "O",
  "P",
  "Q",
  "R",
  "S",
  "T",
  "U",
  "V",
  "W",
  "X",
  "Y",
  "Z",
  "bracketleft",
  "backslash",
  "bracketright",
  "asciicircum",
  "underscore",
  "quoteleft",
  "a",
  "b",
  "c",
  "d",
  "e",
  "f",
  "g",
  "h",
  "i",
  "j",
  "k",
  "l",
  "m",
  "n",
  "o",
  "p",
  "q",
  "r",
  "s",
  "t",
  "u",
  "v",
  "w",
  "x",
  "y",
  "z",
  "braceleft",
  "bar",
  "braceright",
  "asciitilde",
  "exclamdown",
  "cent",
  "sterling",
  "fraction",
  "yen",
  "florin",
  "section",
  "currency",
  "quotesingle",
  "quotedblleft",
  "guillemotleft",
  "guilsinglleft",
  "guilsinglright",
  "fi",
  "fl",
  "endash",
  "dagger",
  "daggerdbl",
  "periodcentered",
  "paragraph",
  "bullet",
  "quotesinglbase",
  "quotedblbase",
  "quotedblright",
  "guillemotright",
  "ellipsis",
  "perthousand",
  "questiondown",
  "grave",
  "acute",
  "circumflex",
  "tilde",
  "macron",
  "breve",
  "dotaccent",
  "dieresis",
  "ring",
  "cedilla",
  "hungarumlaut",
  "ogonek",
  "caron",
  "emdash",
  "AE",
  "ordfeminine",
  "Lslash",
  "Oslash",
  "OE",
  "ordmasculine",
  "ae",
  "dotlessi",
  "lslash",
  "oslash",
  "oe",
  "germandbls",
  "onesuperior",
  "logicalnot",
  "mu",
  "trademark",
  "Eth",
  "onehalf",
  "plusminus",
  "Thorn",
  "onequarter",
  "divide",
  "brokenbar",
  "degree",
  "thorn",
  "threequarters",
  "twosuperior",
  "registered",
  "minus",
  "eth",
  "multiply",
  "threesuperior",
  "copyright",
  "Aacute",
  "Acircumflex",
  "Adieresis",
  "Agrave",
  "Aring",
  "Atilde",
  "Ccedilla",
  "Eacute",
  "Ecircumflex",
  "Edieresis",
  "Egrave",
  "Iacute",
  "Icircumflex",
  "Idieresis",
  "Igrave",
  "Ntilde",
  "Oacute",
  "Ocircumflex",
  "Odieresis",
  "Ograve",
  "Otilde",
  "Scaron",
  "Uacute",
  "Ucircumflex",
  "Udieresis",
  "Ugrave",
  "Yacute",
  "Ydieresis",
  "Zcaron",
  "aacute",
  "acircumflex",
  "adieresis",
  "agrave",
  "aring",
  "atilde",
  "ccedilla",
  "eacute",
  "ecircumflex",
  "edieresis",
  "egrave",
  "iacute",
  "icircumflex",
  "idieresis",
  "igrave",
  "ntilde",
  "oacute",
  "ocircumflex",
  "odieresis",
  "ograve",
  "otilde",
  "scaron",
  "uacute",
  "ucircumflex",
  "udieresis",
  "ugrave",
  "yacute",
  "ydieresis",
  "zcaron",
  "exclamsmall",
  "Hungarumlautsmall",
  "dollaroldstyle",
  "dollarsuperior",
  "ampersandsmall",
  "Acutesmall",
  "parenleftsuperior",
  "parenrightsuperior",
  "266 ff",
  "onedotenleader",
  "zerooldstyle",
  "oneoldstyle",
  "twooldstyle",
  "threeoldstyle",
  "fouroldstyle",
  "fiveoldstyle",
  "sixoldstyle",
  "sevenoldstyle",
  "eightoldstyle",
  "nineoldstyle",
  "commasuperior",
  "threequartersemdash",
  "periodsuperior",
  "questionsmall",
  "asuperior",
  "bsuperior",
  "centsuperior",
  "dsuperior",
  "esuperior",
  "isuperior",
  "lsuperior",
  "msuperior",
  "nsuperior",
  "osuperior",
  "rsuperior",
  "ssuperior",
  "tsuperior",
  "ff",
  "ffi",
  "ffl",
  "parenleftinferior",
  "parenrightinferior",
  "Circumflexsmall",
  "hyphensuperior",
  "Gravesmall",
  "Asmall",
  "Bsmall",
  "Csmall",
  "Dsmall",
  "Esmall",
  "Fsmall",
  "Gsmall",
  "Hsmall",
  "Ismall",
  "Jsmall",
  "Ksmall",
  "Lsmall",
  "Msmall",
  "Nsmall",
  "Osmall",
  "Psmall",
  "Qsmall",
  "Rsmall",
  "Ssmall",
  "Tsmall",
  "Usmall",
  "Vsmall",
  "Wsmall",
  "Xsmall",
  "Ysmall",
  "Zsmall",
  "colonmonetary",
  "onefitted",
  "rupiah",
  "Tildesmall",
  "exclamdownsmall",
  "centoldstyle",
  "Lslashsmall",
  "Scaronsmall",
  "Zcaronsmall",
  "Dieresissmall",
  "Brevesmall",
  "Caronsmall",
  "Dotaccentsmall",
  "Macronsmall",
  "figuredash",
  "hypheninferior",
  "Ogoneksmall",
  "Ringsmall",
  "Cedillasmall",
  "questiondownsmall",
  "oneeighth",
  "threeeighths",
  "fiveeighths",
  "seveneighths",
  "onethird",
  "twothirds",
  "zerosuperior",
  "foursuperior",
  "fivesuperior",
  "sixsuperior",
  "sevensuperior",
  "eightsuperior",
  "ninesuperior",
  "zeroinferior",
  "oneinferior",
  "twoinferior",
  "threeinferior",
  "fourinferior",
  "fiveinferior",
  "sixinferior",
  "seveninferior",
  "eightinferior",
  "nineinferior",
  "centinferior",
  "dollarinferior",
  "periodinferior",
  "commainferior",
  "Agravesmall",
  "Aacutesmall",
  "Acircumflexsmall",
  "Atildesmall",
  "Adieresissmall",
  "Aringsmall",
  "AEsmall",
  "Ccedillasmall",
  "Egravesmall",
  "Eacutesmall",
  "Ecircumflexsmall",
  "Edieresissmall",
  "Igravesmall",
  "Iacutesmall",
  "Icircumflexsmall",
  "Idieresissmall",
  "Ethsmall",
  "Ntildesmall",
  "Ogravesmall",
  "Oacutesmall",
  "Ocircumflexsmall",
  "Otildesmall",
  "Odieresissmall",
  "OEsmall",
  "Oslashsmall",
  "Ugravesmall",
  "Uacutesmall",
  "Ucircumflexsmall",
  "Udieresissmall",
  "Yacutesmall",
  "Thornsmall",
  "Ydieresissmall",
  "001.000",
  "001.001",
  "001.002",
  "001.003",
  "Black",
  "Bold",
  "Book",
  "Light",
  "Medium",
  "Regular",
  "Roman",
  "Semibold"
];
var cffStandardEncoding = [
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "space",
  "exclam",
  "quotedbl",
  "numbersign",
  "dollar",
  "percent",
  "ampersand",
  "quoteright",
  "parenleft",
  "parenright",
  "asterisk",
  "plus",
  "comma",
  "hyphen",
  "period",
  "slash",
  "zero",
  "one",
  "two",
  "three",
  "four",
  "five",
  "six",
  "seven",
  "eight",
  "nine",
  "colon",
  "semicolon",
  "less",
  "equal",
  "greater",
  "question",
  "at",
  "A",
  "B",
  "C",
  "D",
  "E",
  "F",
  "G",
  "H",
  "I",
  "J",
  "K",
  "L",
  "M",
  "N",
  "O",
  "P",
  "Q",
  "R",
  "S",
  "T",
  "U",
  "V",
  "W",
  "X",
  "Y",
  "Z",
  "bracketleft",
  "backslash",
  "bracketright",
  "asciicircum",
  "underscore",
  "quoteleft",
  "a",
  "b",
  "c",
  "d",
  "e",
  "f",
  "g",
  "h",
  "i",
  "j",
  "k",
  "l",
  "m",
  "n",
  "o",
  "p",
  "q",
  "r",
  "s",
  "t",
  "u",
  "v",
  "w",
  "x",
  "y",
  "z",
  "braceleft",
  "bar",
  "braceright",
  "asciitilde",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "exclamdown",
  "cent",
  "sterling",
  "fraction",
  "yen",
  "florin",
  "section",
  "currency",
  "quotesingle",
  "quotedblleft",
  "guillemotleft",
  "guilsinglleft",
  "guilsinglright",
  "fi",
  "fl",
  "",
  "endash",
  "dagger",
  "daggerdbl",
  "periodcentered",
  "",
  "paragraph",
  "bullet",
  "quotesinglbase",
  "quotedblbase",
  "quotedblright",
  "guillemotright",
  "ellipsis",
  "perthousand",
  "",
  "questiondown",
  "",
  "grave",
  "acute",
  "circumflex",
  "tilde",
  "macron",
  "breve",
  "dotaccent",
  "dieresis",
  "",
  "ring",
  "cedilla",
  "",
  "hungarumlaut",
  "ogonek",
  "caron",
  "emdash",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "AE",
  "",
  "ordfeminine",
  "",
  "",
  "",
  "",
  "Lslash",
  "Oslash",
  "OE",
  "ordmasculine",
  "",
  "",
  "",
  "",
  "",
  "ae",
  "",
  "",
  "",
  "dotlessi",
  "",
  "",
  "lslash",
  "oslash",
  "oe",
  "germandbls"
];
var cffExpertEncoding = [
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "space",
  "exclamsmall",
  "Hungarumlautsmall",
  "",
  "dollaroldstyle",
  "dollarsuperior",
  "ampersandsmall",
  "Acutesmall",
  "parenleftsuperior",
  "parenrightsuperior",
  "twodotenleader",
  "onedotenleader",
  "comma",
  "hyphen",
  "period",
  "fraction",
  "zerooldstyle",
  "oneoldstyle",
  "twooldstyle",
  "threeoldstyle",
  "fouroldstyle",
  "fiveoldstyle",
  "sixoldstyle",
  "sevenoldstyle",
  "eightoldstyle",
  "nineoldstyle",
  "colon",
  "semicolon",
  "commasuperior",
  "threequartersemdash",
  "periodsuperior",
  "questionsmall",
  "",
  "asuperior",
  "bsuperior",
  "centsuperior",
  "dsuperior",
  "esuperior",
  "",
  "",
  "isuperior",
  "",
  "",
  "lsuperior",
  "msuperior",
  "nsuperior",
  "osuperior",
  "",
  "",
  "rsuperior",
  "ssuperior",
  "tsuperior",
  "",
  "ff",
  "fi",
  "fl",
  "ffi",
  "ffl",
  "parenleftinferior",
  "",
  "parenrightinferior",
  "Circumflexsmall",
  "hyphensuperior",
  "Gravesmall",
  "Asmall",
  "Bsmall",
  "Csmall",
  "Dsmall",
  "Esmall",
  "Fsmall",
  "Gsmall",
  "Hsmall",
  "Ismall",
  "Jsmall",
  "Ksmall",
  "Lsmall",
  "Msmall",
  "Nsmall",
  "Osmall",
  "Psmall",
  "Qsmall",
  "Rsmall",
  "Ssmall",
  "Tsmall",
  "Usmall",
  "Vsmall",
  "Wsmall",
  "Xsmall",
  "Ysmall",
  "Zsmall",
  "colonmonetary",
  "onefitted",
  "rupiah",
  "Tildesmall",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "",
  "exclamdownsmall",
  "centoldstyle",
  "Lslashsmall",
  "",
  "",
  "Scaronsmall",
  "Zcaronsmall",
  "Dieresissmall",
  "Brevesmall",
  "Caronsmall",
  "",
  "Dotaccentsmall",
  "",
  "",
  "Macronsmall",
  "",
  "",
  "figuredash",
  "hypheninferior",
  "",
  "",
  "Ogoneksmall",
  "Ringsmall",
  "Cedillasmall",
  "",
  "",
  "",
  "onequarter",
  "onehalf",
  "threequarters",
  "questiondownsmall",
  "oneeighth",
  "threeeighths",
  "fiveeighths",
  "seveneighths",
  "onethird",
  "twothirds",
  "",
  "",
  "zerosuperior",
  "onesuperior",
  "twosuperior",
  "threesuperior",
  "foursuperior",
  "fivesuperior",
  "sixsuperior",
  "sevensuperior",
  "eightsuperior",
  "ninesuperior",
  "zeroinferior",
  "oneinferior",
  "twoinferior",
  "threeinferior",
  "fourinferior",
  "fiveinferior",
  "sixinferior",
  "seveninferior",
  "eightinferior",
  "nineinferior",
  "centinferior",
  "dollarinferior",
  "periodinferior",
  "commainferior",
  "Agravesmall",
  "Aacutesmall",
  "Acircumflexsmall",
  "Atildesmall",
  "Adieresissmall",
  "Aringsmall",
  "AEsmall",
  "Ccedillasmall",
  "Egravesmall",
  "Eacutesmall",
  "Ecircumflexsmall",
  "Edieresissmall",
  "Igravesmall",
  "Iacutesmall",
  "Icircumflexsmall",
  "Idieresissmall",
  "Ethsmall",
  "Ntildesmall",
  "Ogravesmall",
  "Oacutesmall",
  "Ocircumflexsmall",
  "Otildesmall",
  "Odieresissmall",
  "OEsmall",
  "Oslashsmall",
  "Ugravesmall",
  "Uacutesmall",
  "Ucircumflexsmall",
  "Udieresissmall",
  "Yacutesmall",
  "Thornsmall",
  "Ydieresissmall"
];
var standardNames = [
  ".notdef",
  ".null",
  "nonmarkingreturn",
  "space",
  "exclam",
  "quotedbl",
  "numbersign",
  "dollar",
  "percent",
  "ampersand",
  "quotesingle",
  "parenleft",
  "parenright",
  "asterisk",
  "plus",
  "comma",
  "hyphen",
  "period",
  "slash",
  "zero",
  "one",
  "two",
  "three",
  "four",
  "five",
  "six",
  "seven",
  "eight",
  "nine",
  "colon",
  "semicolon",
  "less",
  "equal",
  "greater",
  "question",
  "at",
  "A",
  "B",
  "C",
  "D",
  "E",
  "F",
  "G",
  "H",
  "I",
  "J",
  "K",
  "L",
  "M",
  "N",
  "O",
  "P",
  "Q",
  "R",
  "S",
  "T",
  "U",
  "V",
  "W",
  "X",
  "Y",
  "Z",
  "bracketleft",
  "backslash",
  "bracketright",
  "asciicircum",
  "underscore",
  "grave",
  "a",
  "b",
  "c",
  "d",
  "e",
  "f",
  "g",
  "h",
  "i",
  "j",
  "k",
  "l",
  "m",
  "n",
  "o",
  "p",
  "q",
  "r",
  "s",
  "t",
  "u",
  "v",
  "w",
  "x",
  "y",
  "z",
  "braceleft",
  "bar",
  "braceright",
  "asciitilde",
  "Adieresis",
  "Aring",
  "Ccedilla",
  "Eacute",
  "Ntilde",
  "Odieresis",
  "Udieresis",
  "aacute",
  "agrave",
  "acircumflex",
  "adieresis",
  "atilde",
  "aring",
  "ccedilla",
  "eacute",
  "egrave",
  "ecircumflex",
  "edieresis",
  "iacute",
  "igrave",
  "icircumflex",
  "idieresis",
  "ntilde",
  "oacute",
  "ograve",
  "ocircumflex",
  "odieresis",
  "otilde",
  "uacute",
  "ugrave",
  "ucircumflex",
  "udieresis",
  "dagger",
  "degree",
  "cent",
  "sterling",
  "section",
  "bullet",
  "paragraph",
  "germandbls",
  "registered",
  "copyright",
  "trademark",
  "acute",
  "dieresis",
  "notequal",
  "AE",
  "Oslash",
  "infinity",
  "plusminus",
  "lessequal",
  "greaterequal",
  "yen",
  "mu",
  "partialdiff",
  "summation",
  "product",
  "pi",
  "integral",
  "ordfeminine",
  "ordmasculine",
  "Omega",
  "ae",
  "oslash",
  "questiondown",
  "exclamdown",
  "logicalnot",
  "radical",
  "florin",
  "approxequal",
  "Delta",
  "guillemotleft",
  "guillemotright",
  "ellipsis",
  "nonbreakingspace",
  "Agrave",
  "Atilde",
  "Otilde",
  "OE",
  "oe",
  "endash",
  "emdash",
  "quotedblleft",
  "quotedblright",
  "quoteleft",
  "quoteright",
  "divide",
  "lozenge",
  "ydieresis",
  "Ydieresis",
  "fraction",
  "currency",
  "guilsinglleft",
  "guilsinglright",
  "fi",
  "fl",
  "daggerdbl",
  "periodcentered",
  "quotesinglbase",
  "quotedblbase",
  "perthousand",
  "Acircumflex",
  "Ecircumflex",
  "Aacute",
  "Edieresis",
  "Egrave",
  "Iacute",
  "Icircumflex",
  "Idieresis",
  "Igrave",
  "Oacute",
  "Ocircumflex",
  "apple",
  "Ograve",
  "Uacute",
  "Ucircumflex",
  "Ugrave",
  "dotlessi",
  "circumflex",
  "tilde",
  "macron",
  "breve",
  "dotaccent",
  "ring",
  "cedilla",
  "hungarumlaut",
  "ogonek",
  "caron",
  "Lslash",
  "lslash",
  "Scaron",
  "scaron",
  "Zcaron",
  "zcaron",
  "brokenbar",
  "Eth",
  "eth",
  "Yacute",
  "yacute",
  "Thorn",
  "thorn",
  "minus",
  "multiply",
  "onesuperior",
  "twosuperior",
  "threesuperior",
  "onehalf",
  "onequarter",
  "threequarters",
  "franc",
  "Gbreve",
  "gbreve",
  "Idotaccent",
  "Scedilla",
  "scedilla",
  "Cacute",
  "cacute",
  "Ccaron",
  "ccaron",
  "dcroat"
];
function DefaultEncoding(font) {
  this.font = font;
}
DefaultEncoding.prototype.charToGlyphIndex = function(c) {
  var code = c.codePointAt(0);
  var glyphs = this.font.glyphs;
  if (glyphs) {
    for (var i = 0;i < glyphs.length; i += 1) {
      var glyph = glyphs.get(i);
      for (var j = 0;j < glyph.unicodes.length; j += 1) {
        if (glyph.unicodes[j] === code) {
          return i;
        }
      }
    }
  }
  return null;
};
function CmapEncoding(cmap2) {
  this.cmap = cmap2;
}
CmapEncoding.prototype.charToGlyphIndex = function(c) {
  return this.cmap.glyphIndexMap[c.codePointAt(0)] || 0;
};
function CffEncoding(encoding, charset) {
  this.encoding = encoding;
  this.charset = charset;
}
CffEncoding.prototype.charToGlyphIndex = function(s) {
  var code = s.codePointAt(0);
  var charName = this.encoding[code];
  return this.charset.indexOf(charName);
};
function GlyphNames(post2) {
  switch (post2.version) {
    case 1:
      this.names = standardNames.slice();
      break;
    case 2:
      this.names = new Array(post2.numberOfGlyphs);
      for (var i = 0;i < post2.numberOfGlyphs; i++) {
        if (post2.glyphNameIndex[i] < standardNames.length) {
          this.names[i] = standardNames[post2.glyphNameIndex[i]];
        } else {
          this.names[i] = post2.names[post2.glyphNameIndex[i] - standardNames.length];
        }
      }
      break;
    case 2.5:
      this.names = new Array(post2.numberOfGlyphs);
      for (var i$1 = 0;i$1 < post2.numberOfGlyphs; i$1++) {
        this.names[i$1] = standardNames[i$1 + post2.glyphNameIndex[i$1]];
      }
      break;
    case 3:
      this.names = [];
      break;
    default:
      this.names = [];
      break;
  }
}
GlyphNames.prototype.nameToGlyphIndex = function(name) {
  return this.names.indexOf(name);
};
GlyphNames.prototype.glyphIndexToName = function(gid) {
  return this.names[gid];
};
function line(ctx, x1, y1, x2, y2) {
  ctx.beginPath();
  ctx.moveTo(x1, y1);
  ctx.lineTo(x2, y2);
  ctx.stroke();
}
var draw$1 = { line };
function getPathDefinition(glyph, path) {
  var _path = path || new Path;
  return {
    configurable: true,
    get: function() {
      if (typeof _path === "function") {
        _path = _path();
      }
      return _path;
    },
    set: function(p) {
      _path = p;
    }
  };
}
function Glyph(options) {
  this.bindConstructorValues(options);
}
Glyph.prototype.bindConstructorValues = function(options) {
  this.index = options.index || 0;
  this.name = options.name || null;
  this.unicode = options.unicode || undefined;
  this.unicodes = options.unicodes || options.unicode !== undefined ? [options.unicode] : [];
  if ("xMin" in options) {
    this.xMin = options.xMin;
  }
  if ("yMin" in options) {
    this.yMin = options.yMin;
  }
  if ("xMax" in options) {
    this.xMax = options.xMax;
  }
  if ("yMax" in options) {
    this.yMax = options.yMax;
  }
  if ("advanceWidth" in options) {
    this.advanceWidth = options.advanceWidth;
  }
  Object.defineProperty(this, "path", getPathDefinition(this, options.path));
};
Glyph.prototype.addUnicode = function(unicode) {
  if (this.unicodes.length === 0) {
    this.unicode = unicode;
  }
  this.unicodes.push(unicode);
};
Glyph.prototype.getBoundingBox = function() {
  return this.path.getBoundingBox();
};
Glyph.prototype.getPath = function(x, y, fontSize, options, font) {
  x = x !== undefined ? x : 0;
  y = y !== undefined ? y : 0;
  fontSize = fontSize !== undefined ? fontSize : 72;
  var commands;
  var hPoints;
  if (!options) {
    options = {};
  }
  var xScale = options.xScale;
  var yScale = options.yScale;
  if (options.hinting && font && font.hinting) {
    hPoints = this.path && font.hinting.exec(this, fontSize);
  }
  if (hPoints) {
    commands = font.hinting.getCommands(hPoints);
    x = Math.round(x);
    y = Math.round(y);
    xScale = yScale = 1;
  } else {
    commands = this.path.commands;
    var scale2 = 1 / (this.path.unitsPerEm || 1000) * fontSize;
    if (xScale === undefined) {
      xScale = scale2;
    }
    if (yScale === undefined) {
      yScale = scale2;
    }
  }
  var p = new Path;
  for (var i = 0;i < commands.length; i += 1) {
    var cmd = commands[i];
    if (cmd.type === "M") {
      p.moveTo(x + cmd.x * xScale, y + -cmd.y * yScale);
    } else if (cmd.type === "L") {
      p.lineTo(x + cmd.x * xScale, y + -cmd.y * yScale);
    } else if (cmd.type === "Q") {
      p.quadraticCurveTo(x + cmd.x1 * xScale, y + -cmd.y1 * yScale, x + cmd.x * xScale, y + -cmd.y * yScale);
    } else if (cmd.type === "C") {
      p.curveTo(x + cmd.x1 * xScale, y + -cmd.y1 * yScale, x + cmd.x2 * xScale, y + -cmd.y2 * yScale, x + cmd.x * xScale, y + -cmd.y * yScale);
    } else if (cmd.type === "Z") {
      p.closePath();
    }
  }
  return p;
};
Glyph.prototype.getContours = function() {
  if (this.points === undefined) {
    return [];
  }
  var contours = [];
  var currentContour = [];
  for (var i = 0;i < this.points.length; i += 1) {
    var pt = this.points[i];
    currentContour.push(pt);
    if (pt.lastPointOfContour) {
      contours.push(currentContour);
      currentContour = [];
    }
  }
  check.argument(currentContour.length === 0, "There are still points left in the current contour.");
  return contours;
};
Glyph.prototype.getMetrics = function() {
  var commands = this.path.commands;
  var xCoords = [];
  var yCoords = [];
  for (var i = 0;i < commands.length; i += 1) {
    var cmd = commands[i];
    if (cmd.type !== "Z") {
      xCoords.push(cmd.x);
      yCoords.push(cmd.y);
    }
    if (cmd.type === "Q" || cmd.type === "C") {
      xCoords.push(cmd.x1);
      yCoords.push(cmd.y1);
    }
    if (cmd.type === "C") {
      xCoords.push(cmd.x2);
      yCoords.push(cmd.y2);
    }
  }
  var metrics = {
    xMin: Math.min.apply(null, xCoords),
    yMin: Math.min.apply(null, yCoords),
    xMax: Math.max.apply(null, xCoords),
    yMax: Math.max.apply(null, yCoords),
    leftSideBearing: this.leftSideBearing
  };
  if (!isFinite(metrics.xMin)) {
    metrics.xMin = 0;
  }
  if (!isFinite(metrics.xMax)) {
    metrics.xMax = this.advanceWidth;
  }
  if (!isFinite(metrics.yMin)) {
    metrics.yMin = 0;
  }
  if (!isFinite(metrics.yMax)) {
    metrics.yMax = 0;
  }
  metrics.rightSideBearing = this.advanceWidth - metrics.leftSideBearing - (metrics.xMax - metrics.xMin);
  return metrics;
};
Glyph.prototype.draw = function(ctx, x, y, fontSize, options) {
  this.getPath(x, y, fontSize, options).draw(ctx);
};
Glyph.prototype.drawPoints = function(ctx, x, y, fontSize) {
  function drawCircles(l, x2, y2, scale3) {
    ctx.beginPath();
    for (var j = 0;j < l.length; j += 1) {
      ctx.moveTo(x2 + l[j].x * scale3, y2 + l[j].y * scale3);
      ctx.arc(x2 + l[j].x * scale3, y2 + l[j].y * scale3, 2, 0, Math.PI * 2, false);
    }
    ctx.closePath();
    ctx.fill();
  }
  x = x !== undefined ? x : 0;
  y = y !== undefined ? y : 0;
  fontSize = fontSize !== undefined ? fontSize : 24;
  var scale2 = 1 / this.path.unitsPerEm * fontSize;
  var blueCircles = [];
  var redCircles = [];
  var path = this.path;
  for (var i = 0;i < path.commands.length; i += 1) {
    var cmd = path.commands[i];
    if (cmd.x !== undefined) {
      blueCircles.push({ x: cmd.x, y: -cmd.y });
    }
    if (cmd.x1 !== undefined) {
      redCircles.push({ x: cmd.x1, y: -cmd.y1 });
    }
    if (cmd.x2 !== undefined) {
      redCircles.push({ x: cmd.x2, y: -cmd.y2 });
    }
  }
  ctx.fillStyle = "blue";
  drawCircles(blueCircles, x, y, scale2);
  ctx.fillStyle = "red";
  drawCircles(redCircles, x, y, scale2);
};
Glyph.prototype.drawMetrics = function(ctx, x, y, fontSize) {
  var scale2;
  x = x !== undefined ? x : 0;
  y = y !== undefined ? y : 0;
  fontSize = fontSize !== undefined ? fontSize : 24;
  scale2 = 1 / this.path.unitsPerEm * fontSize;
  ctx.lineWidth = 1;
  ctx.strokeStyle = "black";
  draw$1.line(ctx, x, -1e4, x, 1e4);
  draw$1.line(ctx, -1e4, y, 1e4, y);
  var xMin = this.xMin || 0;
  var yMin = this.yMin || 0;
  var xMax = this.xMax || 0;
  var yMax = this.yMax || 0;
  var advanceWidth = this.advanceWidth || 0;
  ctx.strokeStyle = "blue";
  draw$1.line(ctx, x + xMin * scale2, -1e4, x + xMin * scale2, 1e4);
  draw$1.line(ctx, x + xMax * scale2, -1e4, x + xMax * scale2, 1e4);
  draw$1.line(ctx, -1e4, y + -yMin * scale2, 1e4, y + -yMin * scale2);
  draw$1.line(ctx, -1e4, y + -yMax * scale2, 1e4, y + -yMax * scale2);
  ctx.strokeStyle = "green";
  draw$1.line(ctx, x + advanceWidth * scale2, -1e4, x + advanceWidth * scale2, 1e4);
};
function defineDependentProperty(glyph, externalName, internalName) {
  Object.defineProperty(glyph, externalName, {
    get: function() {
      glyph.path;
      return glyph[internalName];
    },
    set: function(newValue) {
      glyph[internalName] = newValue;
    },
    enumerable: true,
    configurable: true
  });
}
function GlyphSet(font, glyphs) {
  this.font = font;
  this.glyphs = {};
  if (Array.isArray(glyphs)) {
    for (var i = 0;i < glyphs.length; i++) {
      var glyph = glyphs[i];
      glyph.path.unitsPerEm = font.unitsPerEm;
      this.glyphs[i] = glyph;
    }
  }
  this.length = glyphs && glyphs.length || 0;
}
GlyphSet.prototype.get = function(index) {
  if (this.glyphs[index] === undefined) {
    this.font._push(index);
    if (typeof this.glyphs[index] === "function") {
      this.glyphs[index] = this.glyphs[index]();
    }
    var glyph = this.glyphs[index];
    var unicodeObj = this.font._IndexToUnicodeMap[index];
    if (unicodeObj) {
      for (var j = 0;j < unicodeObj.unicodes.length; j++) {
        glyph.addUnicode(unicodeObj.unicodes[j]);
      }
    }
    if (this.font.cffEncoding) {
      if (this.font.isCIDFont) {
        glyph.name = "gid" + index;
      } else {
        glyph.name = this.font.cffEncoding.charset[index];
      }
    } else if (this.font.glyphNames.names) {
      glyph.name = this.font.glyphNames.glyphIndexToName(index);
    }
    this.glyphs[index].advanceWidth = this.font._hmtxTableData[index].advanceWidth;
    this.glyphs[index].leftSideBearing = this.font._hmtxTableData[index].leftSideBearing;
  } else {
    if (typeof this.glyphs[index] === "function") {
      this.glyphs[index] = this.glyphs[index]();
    }
  }
  return this.glyphs[index];
};
GlyphSet.prototype.push = function(index, loader) {
  this.glyphs[index] = loader;
  this.length++;
};
function glyphLoader(font, index) {
  return new Glyph({ index, font });
}
function ttfGlyphLoader(font, index, parseGlyph2, data, position, buildPath2) {
  return function() {
    var glyph = new Glyph({ index, font });
    glyph.path = function() {
      parseGlyph2(glyph, data, position);
      var path = buildPath2(font.glyphs, glyph);
      path.unitsPerEm = font.unitsPerEm;
      return path;
    };
    defineDependentProperty(glyph, "xMin", "_xMin");
    defineDependentProperty(glyph, "xMax", "_xMax");
    defineDependentProperty(glyph, "yMin", "_yMin");
    defineDependentProperty(glyph, "yMax", "_yMax");
    return glyph;
  };
}
function cffGlyphLoader(font, index, parseCFFCharstring2, charstring) {
  return function() {
    var glyph = new Glyph({ index, font });
    glyph.path = function() {
      var path = parseCFFCharstring2(font, glyph, charstring);
      path.unitsPerEm = font.unitsPerEm;
      return path;
    };
    return glyph;
  };
}
var glyphset = { GlyphSet, glyphLoader, ttfGlyphLoader, cffGlyphLoader };
function equals(a, b) {
  if (a === b) {
    return true;
  } else if (Array.isArray(a) && Array.isArray(b)) {
    if (a.length !== b.length) {
      return false;
    }
    for (var i = 0;i < a.length; i += 1) {
      if (!equals(a[i], b[i])) {
        return false;
      }
    }
    return true;
  } else {
    return false;
  }
}
function calcCFFSubroutineBias(subrs) {
  var bias;
  if (subrs.length < 1240) {
    bias = 107;
  } else if (subrs.length < 33900) {
    bias = 1131;
  } else {
    bias = 32768;
  }
  return bias;
}
function parseCFFIndex(data, start, conversionFn) {
  var offsets = [];
  var objects = [];
  var count = parse.getCard16(data, start);
  var objectOffset;
  var endOffset;
  if (count !== 0) {
    var offsetSize = parse.getByte(data, start + 2);
    objectOffset = start + (count + 1) * offsetSize + 2;
    var pos = start + 3;
    for (var i = 0;i < count + 1; i += 1) {
      offsets.push(parse.getOffset(data, pos, offsetSize));
      pos += offsetSize;
    }
    endOffset = objectOffset + offsets[count];
  } else {
    endOffset = start + 2;
  }
  for (var i$1 = 0;i$1 < offsets.length - 1; i$1 += 1) {
    var value = parse.getBytes(data, objectOffset + offsets[i$1], objectOffset + offsets[i$1 + 1]);
    if (conversionFn) {
      value = conversionFn(value);
    }
    objects.push(value);
  }
  return { objects, startOffset: start, endOffset };
}
function parseCFFIndexLowMemory(data, start) {
  var offsets = [];
  var count = parse.getCard16(data, start);
  var objectOffset;
  var endOffset;
  if (count !== 0) {
    var offsetSize = parse.getByte(data, start + 2);
    objectOffset = start + (count + 1) * offsetSize + 2;
    var pos = start + 3;
    for (var i = 0;i < count + 1; i += 1) {
      offsets.push(parse.getOffset(data, pos, offsetSize));
      pos += offsetSize;
    }
    endOffset = objectOffset + offsets[count];
  } else {
    endOffset = start + 2;
  }
  return { offsets, startOffset: start, endOffset };
}
function getCffIndexObject(i, offsets, data, start, conversionFn) {
  var count = parse.getCard16(data, start);
  var objectOffset = 0;
  if (count !== 0) {
    var offsetSize = parse.getByte(data, start + 2);
    objectOffset = start + (count + 1) * offsetSize + 2;
  }
  var value = parse.getBytes(data, objectOffset + offsets[i], objectOffset + offsets[i + 1]);
  if (conversionFn) {
    value = conversionFn(value);
  }
  return value;
}
function parseFloatOperand(parser) {
  var s = "";
  var eof = 15;
  var lookup = ["0", "1", "2", "3", "4", "5", "6", "7", "8", "9", ".", "E", "E-", null, "-"];
  while (true) {
    var b = parser.parseByte();
    var n1 = b >> 4;
    var n2 = b & 15;
    if (n1 === eof) {
      break;
    }
    s += lookup[n1];
    if (n2 === eof) {
      break;
    }
    s += lookup[n2];
  }
  return parseFloat(s);
}
function parseOperand(parser, b0) {
  var b1;
  var b2;
  var b3;
  var b4;
  if (b0 === 28) {
    b1 = parser.parseByte();
    b2 = parser.parseByte();
    return b1 << 8 | b2;
  }
  if (b0 === 29) {
    b1 = parser.parseByte();
    b2 = parser.parseByte();
    b3 = parser.parseByte();
    b4 = parser.parseByte();
    return b1 << 24 | b2 << 16 | b3 << 8 | b4;
  }
  if (b0 === 30) {
    return parseFloatOperand(parser);
  }
  if (b0 >= 32 && b0 <= 246) {
    return b0 - 139;
  }
  if (b0 >= 247 && b0 <= 250) {
    b1 = parser.parseByte();
    return (b0 - 247) * 256 + b1 + 108;
  }
  if (b0 >= 251 && b0 <= 254) {
    b1 = parser.parseByte();
    return -(b0 - 251) * 256 - b1 - 108;
  }
  throw new Error("Invalid b0 " + b0);
}
function entriesToObject(entries) {
  var o = {};
  for (var i = 0;i < entries.length; i += 1) {
    var key = entries[i][0];
    var values = entries[i][1];
    var value = undefined;
    if (values.length === 1) {
      value = values[0];
    } else {
      value = values;
    }
    if (o.hasOwnProperty(key) && !isNaN(o[key])) {
      throw new Error("Object " + o + " already has key " + key);
    }
    o[key] = value;
  }
  return o;
}
function parseCFFDict(data, start, size) {
  start = start !== undefined ? start : 0;
  var parser = new parse.Parser(data, start);
  var entries = [];
  var operands = [];
  size = size !== undefined ? size : data.length;
  while (parser.relativeOffset < size) {
    var op = parser.parseByte();
    if (op <= 21) {
      if (op === 12) {
        op = 1200 + parser.parseByte();
      }
      entries.push([op, operands]);
      operands = [];
    } else {
      operands.push(parseOperand(parser, op));
    }
  }
  return entriesToObject(entries);
}
function getCFFString(strings, index) {
  if (index <= 390) {
    index = cffStandardStrings[index];
  } else {
    index = strings[index - 391];
  }
  return index;
}
function interpretDict(dict, meta2, strings) {
  var newDict = {};
  var value;
  for (var i = 0;i < meta2.length; i += 1) {
    var m = meta2[i];
    if (Array.isArray(m.type)) {
      var values = [];
      values.length = m.type.length;
      for (var j = 0;j < m.type.length; j++) {
        value = dict[m.op] !== undefined ? dict[m.op][j] : undefined;
        if (value === undefined) {
          value = m.value !== undefined && m.value[j] !== undefined ? m.value[j] : null;
        }
        if (m.type[j] === "SID") {
          value = getCFFString(strings, value);
        }
        values[j] = value;
      }
      newDict[m.name] = values;
    } else {
      value = dict[m.op];
      if (value === undefined) {
        value = m.value !== undefined ? m.value : null;
      }
      if (m.type === "SID") {
        value = getCFFString(strings, value);
      }
      newDict[m.name] = value;
    }
  }
  return newDict;
}
function parseCFFHeader(data, start) {
  var header = {};
  header.formatMajor = parse.getCard8(data, start);
  header.formatMinor = parse.getCard8(data, start + 1);
  header.size = parse.getCard8(data, start + 2);
  header.offsetSize = parse.getCard8(data, start + 3);
  header.startOffset = start;
  header.endOffset = start + 4;
  return header;
}
var TOP_DICT_META = [
  { name: "version", op: 0, type: "SID" },
  { name: "notice", op: 1, type: "SID" },
  { name: "copyright", op: 1200, type: "SID" },
  { name: "fullName", op: 2, type: "SID" },
  { name: "familyName", op: 3, type: "SID" },
  { name: "weight", op: 4, type: "SID" },
  { name: "isFixedPitch", op: 1201, type: "number", value: 0 },
  { name: "italicAngle", op: 1202, type: "number", value: 0 },
  { name: "underlinePosition", op: 1203, type: "number", value: -100 },
  { name: "underlineThickness", op: 1204, type: "number", value: 50 },
  { name: "paintType", op: 1205, type: "number", value: 0 },
  { name: "charstringType", op: 1206, type: "number", value: 2 },
  {
    name: "fontMatrix",
    op: 1207,
    type: ["real", "real", "real", "real", "real", "real"],
    value: [0.001, 0, 0, 0.001, 0, 0]
  },
  { name: "uniqueId", op: 13, type: "number" },
  { name: "fontBBox", op: 5, type: ["number", "number", "number", "number"], value: [0, 0, 0, 0] },
  { name: "strokeWidth", op: 1208, type: "number", value: 0 },
  { name: "xuid", op: 14, type: [], value: null },
  { name: "charset", op: 15, type: "offset", value: 0 },
  { name: "encoding", op: 16, type: "offset", value: 0 },
  { name: "charStrings", op: 17, type: "offset", value: 0 },
  { name: "private", op: 18, type: ["number", "offset"], value: [0, 0] },
  { name: "ros", op: 1230, type: ["SID", "SID", "number"] },
  { name: "cidFontVersion", op: 1231, type: "number", value: 0 },
  { name: "cidFontRevision", op: 1232, type: "number", value: 0 },
  { name: "cidFontType", op: 1233, type: "number", value: 0 },
  { name: "cidCount", op: 1234, type: "number", value: 8720 },
  { name: "uidBase", op: 1235, type: "number" },
  { name: "fdArray", op: 1236, type: "offset" },
  { name: "fdSelect", op: 1237, type: "offset" },
  { name: "fontName", op: 1238, type: "SID" }
];
var PRIVATE_DICT_META = [
  { name: "subrs", op: 19, type: "offset", value: 0 },
  { name: "defaultWidthX", op: 20, type: "number", value: 0 },
  { name: "nominalWidthX", op: 21, type: "number", value: 0 }
];
function parseCFFTopDict(data, strings) {
  var dict = parseCFFDict(data, 0, data.byteLength);
  return interpretDict(dict, TOP_DICT_META, strings);
}
function parseCFFPrivateDict(data, start, size, strings) {
  var dict = parseCFFDict(data, start, size);
  return interpretDict(dict, PRIVATE_DICT_META, strings);
}
function gatherCFFTopDicts(data, start, cffIndex, strings) {
  var topDictArray = [];
  for (var iTopDict = 0;iTopDict < cffIndex.length; iTopDict += 1) {
    var topDictData = new DataView(new Uint8Array(cffIndex[iTopDict]).buffer);
    var topDict = parseCFFTopDict(topDictData, strings);
    topDict._subrs = [];
    topDict._subrsBias = 0;
    topDict._defaultWidthX = 0;
    topDict._nominalWidthX = 0;
    var privateSize = topDict.private[0];
    var privateOffset = topDict.private[1];
    if (privateSize !== 0 && privateOffset !== 0) {
      var privateDict = parseCFFPrivateDict(data, privateOffset + start, privateSize, strings);
      topDict._defaultWidthX = privateDict.defaultWidthX;
      topDict._nominalWidthX = privateDict.nominalWidthX;
      if (privateDict.subrs !== 0) {
        var subrOffset = privateOffset + privateDict.subrs;
        var subrIndex = parseCFFIndex(data, subrOffset + start);
        topDict._subrs = subrIndex.objects;
        topDict._subrsBias = calcCFFSubroutineBias(topDict._subrs);
      }
      topDict._privateDict = privateDict;
    }
    topDictArray.push(topDict);
  }
  return topDictArray;
}
function parseCFFCharset(data, start, nGlyphs, strings) {
  var sid;
  var count;
  var parser = new parse.Parser(data, start);
  nGlyphs -= 1;
  var charset = [".notdef"];
  var format = parser.parseCard8();
  if (format === 0) {
    for (var i = 0;i < nGlyphs; i += 1) {
      sid = parser.parseSID();
      charset.push(getCFFString(strings, sid));
    }
  } else if (format === 1) {
    while (charset.length <= nGlyphs) {
      sid = parser.parseSID();
      count = parser.parseCard8();
      for (var i$1 = 0;i$1 <= count; i$1 += 1) {
        charset.push(getCFFString(strings, sid));
        sid += 1;
      }
    }
  } else if (format === 2) {
    while (charset.length <= nGlyphs) {
      sid = parser.parseSID();
      count = parser.parseCard16();
      for (var i$2 = 0;i$2 <= count; i$2 += 1) {
        charset.push(getCFFString(strings, sid));
        sid += 1;
      }
    }
  } else {
    throw new Error("Unknown charset format " + format);
  }
  return charset;
}
function parseCFFEncoding(data, start, charset) {
  var code;
  var enc = {};
  var parser = new parse.Parser(data, start);
  var format = parser.parseCard8();
  if (format === 0) {
    var nCodes = parser.parseCard8();
    for (var i = 0;i < nCodes; i += 1) {
      code = parser.parseCard8();
      enc[code] = i;
    }
  } else if (format === 1) {
    var nRanges = parser.parseCard8();
    code = 1;
    for (var i$1 = 0;i$1 < nRanges; i$1 += 1) {
      var first = parser.parseCard8();
      var nLeft = parser.parseCard8();
      for (var j = first;j <= first + nLeft; j += 1) {
        enc[j] = code;
        code += 1;
      }
    }
  } else {
    throw new Error("Unknown encoding format " + format);
  }
  return new CffEncoding(enc, charset);
}
function parseCFFCharstring(font, glyph, code) {
  var c1x;
  var c1y;
  var c2x;
  var c2y;
  var p = new Path;
  var stack = [];
  var nStems = 0;
  var haveWidth = false;
  var open = false;
  var x = 0;
  var y = 0;
  var subrs;
  var subrsBias;
  var defaultWidthX;
  var nominalWidthX;
  if (font.isCIDFont) {
    var fdIndex = font.tables.cff.topDict._fdSelect[glyph.index];
    var fdDict = font.tables.cff.topDict._fdArray[fdIndex];
    subrs = fdDict._subrs;
    subrsBias = fdDict._subrsBias;
    defaultWidthX = fdDict._defaultWidthX;
    nominalWidthX = fdDict._nominalWidthX;
  } else {
    subrs = font.tables.cff.topDict._subrs;
    subrsBias = font.tables.cff.topDict._subrsBias;
    defaultWidthX = font.tables.cff.topDict._defaultWidthX;
    nominalWidthX = font.tables.cff.topDict._nominalWidthX;
  }
  var width = defaultWidthX;
  function newContour(x2, y2) {
    if (open) {
      p.closePath();
    }
    p.moveTo(x2, y2);
    open = true;
  }
  function parseStems() {
    var hasWidthArg;
    hasWidthArg = stack.length % 2 !== 0;
    if (hasWidthArg && !haveWidth) {
      width = stack.shift() + nominalWidthX;
    }
    nStems += stack.length >> 1;
    stack.length = 0;
    haveWidth = true;
  }
  function parse2(code2) {
    var b1;
    var b2;
    var b3;
    var b4;
    var codeIndex;
    var subrCode;
    var jpx;
    var jpy;
    var c3x;
    var c3y;
    var c4x;
    var c4y;
    var i = 0;
    while (i < code2.length) {
      var v = code2[i];
      i += 1;
      switch (v) {
        case 1:
          parseStems();
          break;
        case 3:
          parseStems();
          break;
        case 4:
          if (stack.length > 1 && !haveWidth) {
            width = stack.shift() + nominalWidthX;
            haveWidth = true;
          }
          y += stack.pop();
          newContour(x, y);
          break;
        case 5:
          while (stack.length > 0) {
            x += stack.shift();
            y += stack.shift();
            p.lineTo(x, y);
          }
          break;
        case 6:
          while (stack.length > 0) {
            x += stack.shift();
            p.lineTo(x, y);
            if (stack.length === 0) {
              break;
            }
            y += stack.shift();
            p.lineTo(x, y);
          }
          break;
        case 7:
          while (stack.length > 0) {
            y += stack.shift();
            p.lineTo(x, y);
            if (stack.length === 0) {
              break;
            }
            x += stack.shift();
            p.lineTo(x, y);
          }
          break;
        case 8:
          while (stack.length > 0) {
            c1x = x + stack.shift();
            c1y = y + stack.shift();
            c2x = c1x + stack.shift();
            c2y = c1y + stack.shift();
            x = c2x + stack.shift();
            y = c2y + stack.shift();
            p.curveTo(c1x, c1y, c2x, c2y, x, y);
          }
          break;
        case 10:
          codeIndex = stack.pop() + subrsBias;
          subrCode = subrs[codeIndex];
          if (subrCode) {
            parse2(subrCode);
          }
          break;
        case 11:
          return;
        case 12:
          v = code2[i];
          i += 1;
          switch (v) {
            case 35:
              c1x = x + stack.shift();
              c1y = y + stack.shift();
              c2x = c1x + stack.shift();
              c2y = c1y + stack.shift();
              jpx = c2x + stack.shift();
              jpy = c2y + stack.shift();
              c3x = jpx + stack.shift();
              c3y = jpy + stack.shift();
              c4x = c3x + stack.shift();
              c4y = c3y + stack.shift();
              x = c4x + stack.shift();
              y = c4y + stack.shift();
              stack.shift();
              p.curveTo(c1x, c1y, c2x, c2y, jpx, jpy);
              p.curveTo(c3x, c3y, c4x, c4y, x, y);
              break;
            case 34:
              c1x = x + stack.shift();
              c1y = y;
              c2x = c1x + stack.shift();
              c2y = c1y + stack.shift();
              jpx = c2x + stack.shift();
              jpy = c2y;
              c3x = jpx + stack.shift();
              c3y = c2y;
              c4x = c3x + stack.shift();
              c4y = y;
              x = c4x + stack.shift();
              p.curveTo(c1x, c1y, c2x, c2y, jpx, jpy);
              p.curveTo(c3x, c3y, c4x, c4y, x, y);
              break;
            case 36:
              c1x = x + stack.shift();
              c1y = y + stack.shift();
              c2x = c1x + stack.shift();
              c2y = c1y + stack.shift();
              jpx = c2x + stack.shift();
              jpy = c2y;
              c3x = jpx + stack.shift();
              c3y = c2y;
              c4x = c3x + stack.shift();
              c4y = c3y + stack.shift();
              x = c4x + stack.shift();
              p.curveTo(c1x, c1y, c2x, c2y, jpx, jpy);
              p.curveTo(c3x, c3y, c4x, c4y, x, y);
              break;
            case 37:
              c1x = x + stack.shift();
              c1y = y + stack.shift();
              c2x = c1x + stack.shift();
              c2y = c1y + stack.shift();
              jpx = c2x + stack.shift();
              jpy = c2y + stack.shift();
              c3x = jpx + stack.shift();
              c3y = jpy + stack.shift();
              c4x = c3x + stack.shift();
              c4y = c3y + stack.shift();
              if (Math.abs(c4x - x) > Math.abs(c4y - y)) {
                x = c4x + stack.shift();
              } else {
                y = c4y + stack.shift();
              }
              p.curveTo(c1x, c1y, c2x, c2y, jpx, jpy);
              p.curveTo(c3x, c3y, c4x, c4y, x, y);
              break;
            default:
              console.log("Glyph " + glyph.index + ": unknown operator " + 1200 + v);
              stack.length = 0;
          }
          break;
        case 14:
          if (stack.length > 0 && !haveWidth) {
            width = stack.shift() + nominalWidthX;
            haveWidth = true;
          }
          if (open) {
            p.closePath();
            open = false;
          }
          break;
        case 18:
          parseStems();
          break;
        case 19:
        case 20:
          parseStems();
          i += nStems + 7 >> 3;
          break;
        case 21:
          if (stack.length > 2 && !haveWidth) {
            width = stack.shift() + nominalWidthX;
            haveWidth = true;
          }
          y += stack.pop();
          x += stack.pop();
          newContour(x, y);
          break;
        case 22:
          if (stack.length > 1 && !haveWidth) {
            width = stack.shift() + nominalWidthX;
            haveWidth = true;
          }
          x += stack.pop();
          newContour(x, y);
          break;
        case 23:
          parseStems();
          break;
        case 24:
          while (stack.length > 2) {
            c1x = x + stack.shift();
            c1y = y + stack.shift();
            c2x = c1x + stack.shift();
            c2y = c1y + stack.shift();
            x = c2x + stack.shift();
            y = c2y + stack.shift();
            p.curveTo(c1x, c1y, c2x, c2y, x, y);
          }
          x += stack.shift();
          y += stack.shift();
          p.lineTo(x, y);
          break;
        case 25:
          while (stack.length > 6) {
            x += stack.shift();
            y += stack.shift();
            p.lineTo(x, y);
          }
          c1x = x + stack.shift();
          c1y = y + stack.shift();
          c2x = c1x + stack.shift();
          c2y = c1y + stack.shift();
          x = c2x + stack.shift();
          y = c2y + stack.shift();
          p.curveTo(c1x, c1y, c2x, c2y, x, y);
          break;
        case 26:
          if (stack.length % 2) {
            x += stack.shift();
          }
          while (stack.length > 0) {
            c1x = x;
            c1y = y + stack.shift();
            c2x = c1x + stack.shift();
            c2y = c1y + stack.shift();
            x = c2x;
            y = c2y + stack.shift();
            p.curveTo(c1x, c1y, c2x, c2y, x, y);
          }
          break;
        case 27:
          if (stack.length % 2) {
            y += stack.shift();
          }
          while (stack.length > 0) {
            c1x = x + stack.shift();
            c1y = y;
            c2x = c1x + stack.shift();
            c2y = c1y + stack.shift();
            x = c2x + stack.shift();
            y = c2y;
            p.curveTo(c1x, c1y, c2x, c2y, x, y);
          }
          break;
        case 28:
          b1 = code2[i];
          b2 = code2[i + 1];
          stack.push((b1 << 24 | b2 << 16) >> 16);
          i += 2;
          break;
        case 29:
          codeIndex = stack.pop() + font.gsubrsBias;
          subrCode = font.gsubrs[codeIndex];
          if (subrCode) {
            parse2(subrCode);
          }
          break;
        case 30:
          while (stack.length > 0) {
            c1x = x;
            c1y = y + stack.shift();
            c2x = c1x + stack.shift();
            c2y = c1y + stack.shift();
            x = c2x + stack.shift();
            y = c2y + (stack.length === 1 ? stack.shift() : 0);
            p.curveTo(c1x, c1y, c2x, c2y, x, y);
            if (stack.length === 0) {
              break;
            }
            c1x = x + stack.shift();
            c1y = y;
            c2x = c1x + stack.shift();
            c2y = c1y + stack.shift();
            y = c2y + stack.shift();
            x = c2x + (stack.length === 1 ? stack.shift() : 0);
            p.curveTo(c1x, c1y, c2x, c2y, x, y);
          }
          break;
        case 31:
          while (stack.length > 0) {
            c1x = x + stack.shift();
            c1y = y;
            c2x = c1x + stack.shift();
            c2y = c1y + stack.shift();
            y = c2y + stack.shift();
            x = c2x + (stack.length === 1 ? stack.shift() : 0);
            p.curveTo(c1x, c1y, c2x, c2y, x, y);
            if (stack.length === 0) {
              break;
            }
            c1x = x;
            c1y = y + stack.shift();
            c2x = c1x + stack.shift();
            c2y = c1y + stack.shift();
            x = c2x + stack.shift();
            y = c2y + (stack.length === 1 ? stack.shift() : 0);
            p.curveTo(c1x, c1y, c2x, c2y, x, y);
          }
          break;
        default:
          if (v < 32) {
            console.log("Glyph " + glyph.index + ": unknown operator " + v);
          } else if (v < 247) {
            stack.push(v - 139);
          } else if (v < 251) {
            b1 = code2[i];
            i += 1;
            stack.push((v - 247) * 256 + b1 + 108);
          } else if (v < 255) {
            b1 = code2[i];
            i += 1;
            stack.push(-(v - 251) * 256 - b1 - 108);
          } else {
            b1 = code2[i];
            b2 = code2[i + 1];
            b3 = code2[i + 2];
            b4 = code2[i + 3];
            i += 4;
            stack.push((b1 << 24 | b2 << 16 | b3 << 8 | b4) / 65536);
          }
      }
    }
  }
  parse2(code);
  glyph.advanceWidth = width;
  return p;
}
function parseCFFFDSelect(data, start, nGlyphs, fdArrayCount) {
  var fdSelect = [];
  var fdIndex;
  var parser = new parse.Parser(data, start);
  var format = parser.parseCard8();
  if (format === 0) {
    for (var iGid = 0;iGid < nGlyphs; iGid++) {
      fdIndex = parser.parseCard8();
      if (fdIndex >= fdArrayCount) {
        throw new Error("CFF table CID Font FDSelect has bad FD index value " + fdIndex + " (FD count " + fdArrayCount + ")");
      }
      fdSelect.push(fdIndex);
    }
  } else if (format === 3) {
    var nRanges = parser.parseCard16();
    var first = parser.parseCard16();
    if (first !== 0) {
      throw new Error("CFF Table CID Font FDSelect format 3 range has bad initial GID " + first);
    }
    var next;
    for (var iRange = 0;iRange < nRanges; iRange++) {
      fdIndex = parser.parseCard8();
      next = parser.parseCard16();
      if (fdIndex >= fdArrayCount) {
        throw new Error("CFF table CID Font FDSelect has bad FD index value " + fdIndex + " (FD count " + fdArrayCount + ")");
      }
      if (next > nGlyphs) {
        throw new Error("CFF Table CID Font FDSelect format 3 range has bad GID " + next);
      }
      for (;first < next; first++) {
        fdSelect.push(fdIndex);
      }
      first = next;
    }
    if (next !== nGlyphs) {
      throw new Error("CFF Table CID Font FDSelect format 3 range has bad final GID " + next);
    }
  } else {
    throw new Error("CFF Table CID Font FDSelect table has unsupported format " + format);
  }
  return fdSelect;
}
function parseCFFTable(data, start, font, opt) {
  font.tables.cff = {};
  var header = parseCFFHeader(data, start);
  var nameIndex = parseCFFIndex(data, header.endOffset, parse.bytesToString);
  var topDictIndex = parseCFFIndex(data, nameIndex.endOffset);
  var stringIndex = parseCFFIndex(data, topDictIndex.endOffset, parse.bytesToString);
  var globalSubrIndex = parseCFFIndex(data, stringIndex.endOffset);
  font.gsubrs = globalSubrIndex.objects;
  font.gsubrsBias = calcCFFSubroutineBias(font.gsubrs);
  var topDictArray = gatherCFFTopDicts(data, start, topDictIndex.objects, stringIndex.objects);
  if (topDictArray.length !== 1) {
    throw new Error("CFF table has too many fonts in 'FontSet' - count of fonts NameIndex.length = " + topDictArray.length);
  }
  var topDict = topDictArray[0];
  font.tables.cff.topDict = topDict;
  if (topDict._privateDict) {
    font.defaultWidthX = topDict._privateDict.defaultWidthX;
    font.nominalWidthX = topDict._privateDict.nominalWidthX;
  }
  if (topDict.ros[0] !== undefined && topDict.ros[1] !== undefined) {
    font.isCIDFont = true;
  }
  if (font.isCIDFont) {
    var fdArrayOffset = topDict.fdArray;
    var fdSelectOffset = topDict.fdSelect;
    if (fdArrayOffset === 0 || fdSelectOffset === 0) {
      throw new Error("Font is marked as a CID font, but FDArray and/or FDSelect information is missing");
    }
    fdArrayOffset += start;
    var fdArrayIndex = parseCFFIndex(data, fdArrayOffset);
    var fdArray = gatherCFFTopDicts(data, start, fdArrayIndex.objects, stringIndex.objects);
    topDict._fdArray = fdArray;
    fdSelectOffset += start;
    topDict._fdSelect = parseCFFFDSelect(data, fdSelectOffset, font.numGlyphs, fdArray.length);
  }
  var privateDictOffset = start + topDict.private[1];
  var privateDict = parseCFFPrivateDict(data, privateDictOffset, topDict.private[0], stringIndex.objects);
  font.defaultWidthX = privateDict.defaultWidthX;
  font.nominalWidthX = privateDict.nominalWidthX;
  if (privateDict.subrs !== 0) {
    var subrOffset = privateDictOffset + privateDict.subrs;
    var subrIndex = parseCFFIndex(data, subrOffset);
    font.subrs = subrIndex.objects;
    font.subrsBias = calcCFFSubroutineBias(font.subrs);
  } else {
    font.subrs = [];
    font.subrsBias = 0;
  }
  var charStringsIndex;
  if (opt.lowMemory) {
    charStringsIndex = parseCFFIndexLowMemory(data, start + topDict.charStrings);
    font.nGlyphs = charStringsIndex.offsets.length;
  } else {
    charStringsIndex = parseCFFIndex(data, start + topDict.charStrings);
    font.nGlyphs = charStringsIndex.objects.length;
  }
  var charset = parseCFFCharset(data, start + topDict.charset, font.nGlyphs, stringIndex.objects);
  if (topDict.encoding === 0) {
    font.cffEncoding = new CffEncoding(cffStandardEncoding, charset);
  } else if (topDict.encoding === 1) {
    font.cffEncoding = new CffEncoding(cffExpertEncoding, charset);
  } else {
    font.cffEncoding = parseCFFEncoding(data, start + topDict.encoding, charset);
  }
  font.encoding = font.encoding || font.cffEncoding;
  font.glyphs = new glyphset.GlyphSet(font);
  if (opt.lowMemory) {
    font._push = function(i2) {
      var charString2 = getCffIndexObject(i2, charStringsIndex.offsets, data, start + topDict.charStrings);
      font.glyphs.push(i2, glyphset.cffGlyphLoader(font, i2, parseCFFCharstring, charString2));
    };
  } else {
    for (var i = 0;i < font.nGlyphs; i += 1) {
      var charString = charStringsIndex.objects[i];
      font.glyphs.push(i, glyphset.cffGlyphLoader(font, i, parseCFFCharstring, charString));
    }
  }
}
function encodeString(s, strings) {
  var sid;
  var i = cffStandardStrings.indexOf(s);
  if (i >= 0) {
    sid = i;
  }
  i = strings.indexOf(s);
  if (i >= 0) {
    sid = i + cffStandardStrings.length;
  } else {
    sid = cffStandardStrings.length + strings.length;
    strings.push(s);
  }
  return sid;
}
function makeHeader() {
  return new table.Record("Header", [
    { name: "major", type: "Card8", value: 1 },
    { name: "minor", type: "Card8", value: 0 },
    { name: "hdrSize", type: "Card8", value: 4 },
    { name: "major", type: "Card8", value: 1 }
  ]);
}
function makeNameIndex(fontNames) {
  var t = new table.Record("Name INDEX", [
    { name: "names", type: "INDEX", value: [] }
  ]);
  t.names = [];
  for (var i = 0;i < fontNames.length; i += 1) {
    t.names.push({ name: "name_" + i, type: "NAME", value: fontNames[i] });
  }
  return t;
}
function makeDict(meta2, attrs, strings) {
  var m = {};
  for (var i = 0;i < meta2.length; i += 1) {
    var entry = meta2[i];
    var value = attrs[entry.name];
    if (value !== undefined && !equals(value, entry.value)) {
      if (entry.type === "SID") {
        value = encodeString(value, strings);
      }
      m[entry.op] = { name: entry.name, type: entry.type, value };
    }
  }
  return m;
}
function makeTopDict(attrs, strings) {
  var t = new table.Record("Top DICT", [
    { name: "dict", type: "DICT", value: {} }
  ]);
  t.dict = makeDict(TOP_DICT_META, attrs, strings);
  return t;
}
function makeTopDictIndex(topDict) {
  var t = new table.Record("Top DICT INDEX", [
    { name: "topDicts", type: "INDEX", value: [] }
  ]);
  t.topDicts = [{ name: "topDict_0", type: "TABLE", value: topDict }];
  return t;
}
function makeStringIndex(strings) {
  var t = new table.Record("String INDEX", [
    { name: "strings", type: "INDEX", value: [] }
  ]);
  t.strings = [];
  for (var i = 0;i < strings.length; i += 1) {
    t.strings.push({ name: "string_" + i, type: "STRING", value: strings[i] });
  }
  return t;
}
function makeGlobalSubrIndex() {
  return new table.Record("Global Subr INDEX", [
    { name: "subrs", type: "INDEX", value: [] }
  ]);
}
function makeCharsets(glyphNames, strings) {
  var t = new table.Record("Charsets", [
    { name: "format", type: "Card8", value: 0 }
  ]);
  for (var i = 0;i < glyphNames.length; i += 1) {
    var glyphName = glyphNames[i];
    var glyphSID = encodeString(glyphName, strings);
    t.fields.push({ name: "glyph_" + i, type: "SID", value: glyphSID });
  }
  return t;
}
function glyphToOps(glyph) {
  var ops = [];
  var path = glyph.path;
  ops.push({ name: "width", type: "NUMBER", value: glyph.advanceWidth });
  var x = 0;
  var y = 0;
  for (var i = 0;i < path.commands.length; i += 1) {
    var dx = undefined;
    var dy = undefined;
    var cmd = path.commands[i];
    if (cmd.type === "Q") {
      var _13 = 1 / 3;
      var _23 = 2 / 3;
      cmd = {
        type: "C",
        x: cmd.x,
        y: cmd.y,
        x1: Math.round(_13 * x + _23 * cmd.x1),
        y1: Math.round(_13 * y + _23 * cmd.y1),
        x2: Math.round(_13 * cmd.x + _23 * cmd.x1),
        y2: Math.round(_13 * cmd.y + _23 * cmd.y1)
      };
    }
    if (cmd.type === "M") {
      dx = Math.round(cmd.x - x);
      dy = Math.round(cmd.y - y);
      ops.push({ name: "dx", type: "NUMBER", value: dx });
      ops.push({ name: "dy", type: "NUMBER", value: dy });
      ops.push({ name: "rmoveto", type: "OP", value: 21 });
      x = Math.round(cmd.x);
      y = Math.round(cmd.y);
    } else if (cmd.type === "L") {
      dx = Math.round(cmd.x - x);
      dy = Math.round(cmd.y - y);
      ops.push({ name: "dx", type: "NUMBER", value: dx });
      ops.push({ name: "dy", type: "NUMBER", value: dy });
      ops.push({ name: "rlineto", type: "OP", value: 5 });
      x = Math.round(cmd.x);
      y = Math.round(cmd.y);
    } else if (cmd.type === "C") {
      var dx1 = Math.round(cmd.x1 - x);
      var dy1 = Math.round(cmd.y1 - y);
      var dx2 = Math.round(cmd.x2 - cmd.x1);
      var dy2 = Math.round(cmd.y2 - cmd.y1);
      dx = Math.round(cmd.x - cmd.x2);
      dy = Math.round(cmd.y - cmd.y2);
      ops.push({ name: "dx1", type: "NUMBER", value: dx1 });
      ops.push({ name: "dy1", type: "NUMBER", value: dy1 });
      ops.push({ name: "dx2", type: "NUMBER", value: dx2 });
      ops.push({ name: "dy2", type: "NUMBER", value: dy2 });
      ops.push({ name: "dx", type: "NUMBER", value: dx });
      ops.push({ name: "dy", type: "NUMBER", value: dy });
      ops.push({ name: "rrcurveto", type: "OP", value: 8 });
      x = Math.round(cmd.x);
      y = Math.round(cmd.y);
    }
  }
  ops.push({ name: "endchar", type: "OP", value: 14 });
  return ops;
}
function makeCharStringsIndex(glyphs) {
  var t = new table.Record("CharStrings INDEX", [
    { name: "charStrings", type: "INDEX", value: [] }
  ]);
  for (var i = 0;i < glyphs.length; i += 1) {
    var glyph = glyphs.get(i);
    var ops = glyphToOps(glyph);
    t.charStrings.push({ name: glyph.name, type: "CHARSTRING", value: ops });
  }
  return t;
}
function makePrivateDict(attrs, strings) {
  var t = new table.Record("Private DICT", [
    { name: "dict", type: "DICT", value: {} }
  ]);
  t.dict = makeDict(PRIVATE_DICT_META, attrs, strings);
  return t;
}
function makeCFFTable(glyphs, options) {
  var t = new table.Table("CFF ", [
    { name: "header", type: "RECORD" },
    { name: "nameIndex", type: "RECORD" },
    { name: "topDictIndex", type: "RECORD" },
    { name: "stringIndex", type: "RECORD" },
    { name: "globalSubrIndex", type: "RECORD" },
    { name: "charsets", type: "RECORD" },
    { name: "charStringsIndex", type: "RECORD" },
    { name: "privateDict", type: "RECORD" }
  ]);
  var fontScale = 1 / options.unitsPerEm;
  var attrs = {
    version: options.version,
    fullName: options.fullName,
    familyName: options.familyName,
    weight: options.weightName,
    fontBBox: options.fontBBox || [0, 0, 0, 0],
    fontMatrix: [fontScale, 0, 0, fontScale, 0, 0],
    charset: 999,
    encoding: 0,
    charStrings: 999,
    private: [0, 999]
  };
  var privateAttrs = {};
  var glyphNames = [];
  var glyph;
  for (var i = 1;i < glyphs.length; i += 1) {
    glyph = glyphs.get(i);
    glyphNames.push(glyph.name);
  }
  var strings = [];
  t.header = makeHeader();
  t.nameIndex = makeNameIndex([options.postScriptName]);
  var topDict = makeTopDict(attrs, strings);
  t.topDictIndex = makeTopDictIndex(topDict);
  t.globalSubrIndex = makeGlobalSubrIndex();
  t.charsets = makeCharsets(glyphNames, strings);
  t.charStringsIndex = makeCharStringsIndex(glyphs);
  t.privateDict = makePrivateDict(privateAttrs, strings);
  t.stringIndex = makeStringIndex(strings);
  var startOffset = t.header.sizeOf() + t.nameIndex.sizeOf() + t.topDictIndex.sizeOf() + t.stringIndex.sizeOf() + t.globalSubrIndex.sizeOf();
  attrs.charset = startOffset;
  attrs.encoding = 0;
  attrs.charStrings = attrs.charset + t.charsets.sizeOf();
  attrs.private[1] = attrs.charStrings + t.charStringsIndex.sizeOf();
  topDict = makeTopDict(attrs, strings);
  t.topDictIndex = makeTopDictIndex(topDict);
  return t;
}
var cff = { parse: parseCFFTable, make: makeCFFTable };
function parseHeadTable(data, start) {
  var head2 = {};
  var p = new parse.Parser(data, start);
  head2.version = p.parseVersion();
  head2.fontRevision = Math.round(p.parseFixed() * 1000) / 1000;
  head2.checkSumAdjustment = p.parseULong();
  head2.magicNumber = p.parseULong();
  check.argument(head2.magicNumber === 1594834165, "Font header has wrong magic number.");
  head2.flags = p.parseUShort();
  head2.unitsPerEm = p.parseUShort();
  head2.created = p.parseLongDateTime();
  head2.modified = p.parseLongDateTime();
  head2.xMin = p.parseShort();
  head2.yMin = p.parseShort();
  head2.xMax = p.parseShort();
  head2.yMax = p.parseShort();
  head2.macStyle = p.parseUShort();
  head2.lowestRecPPEM = p.parseUShort();
  head2.fontDirectionHint = p.parseShort();
  head2.indexToLocFormat = p.parseShort();
  head2.glyphDataFormat = p.parseShort();
  return head2;
}
function makeHeadTable(options) {
  var timestamp = Math.round(new Date().getTime() / 1000) + 2082844800;
  var createdTimestamp = timestamp;
  if (options.createdTimestamp) {
    createdTimestamp = options.createdTimestamp + 2082844800;
  }
  return new table.Table("head", [
    { name: "version", type: "FIXED", value: 65536 },
    { name: "fontRevision", type: "FIXED", value: 65536 },
    { name: "checkSumAdjustment", type: "ULONG", value: 0 },
    { name: "magicNumber", type: "ULONG", value: 1594834165 },
    { name: "flags", type: "USHORT", value: 0 },
    { name: "unitsPerEm", type: "USHORT", value: 1000 },
    { name: "created", type: "LONGDATETIME", value: createdTimestamp },
    { name: "modified", type: "LONGDATETIME", value: timestamp },
    { name: "xMin", type: "SHORT", value: 0 },
    { name: "yMin", type: "SHORT", value: 0 },
    { name: "xMax", type: "SHORT", value: 0 },
    { name: "yMax", type: "SHORT", value: 0 },
    { name: "macStyle", type: "USHORT", value: 0 },
    { name: "lowestRecPPEM", type: "USHORT", value: 0 },
    { name: "fontDirectionHint", type: "SHORT", value: 2 },
    { name: "indexToLocFormat", type: "SHORT", value: 0 },
    { name: "glyphDataFormat", type: "SHORT", value: 0 }
  ], options);
}
var head = { parse: parseHeadTable, make: makeHeadTable };
function parseHheaTable(data, start) {
  var hhea2 = {};
  var p = new parse.Parser(data, start);
  hhea2.version = p.parseVersion();
  hhea2.ascender = p.parseShort();
  hhea2.descender = p.parseShort();
  hhea2.lineGap = p.parseShort();
  hhea2.advanceWidthMax = p.parseUShort();
  hhea2.minLeftSideBearing = p.parseShort();
  hhea2.minRightSideBearing = p.parseShort();
  hhea2.xMaxExtent = p.parseShort();
  hhea2.caretSlopeRise = p.parseShort();
  hhea2.caretSlopeRun = p.parseShort();
  hhea2.caretOffset = p.parseShort();
  p.relativeOffset += 8;
  hhea2.metricDataFormat = p.parseShort();
  hhea2.numberOfHMetrics = p.parseUShort();
  return hhea2;
}
function makeHheaTable(options) {
  return new table.Table("hhea", [
    { name: "version", type: "FIXED", value: 65536 },
    { name: "ascender", type: "FWORD", value: 0 },
    { name: "descender", type: "FWORD", value: 0 },
    { name: "lineGap", type: "FWORD", value: 0 },
    { name: "advanceWidthMax", type: "UFWORD", value: 0 },
    { name: "minLeftSideBearing", type: "FWORD", value: 0 },
    { name: "minRightSideBearing", type: "FWORD", value: 0 },
    { name: "xMaxExtent", type: "FWORD", value: 0 },
    { name: "caretSlopeRise", type: "SHORT", value: 1 },
    { name: "caretSlopeRun", type: "SHORT", value: 0 },
    { name: "caretOffset", type: "SHORT", value: 0 },
    { name: "reserved1", type: "SHORT", value: 0 },
    { name: "reserved2", type: "SHORT", value: 0 },
    { name: "reserved3", type: "SHORT", value: 0 },
    { name: "reserved4", type: "SHORT", value: 0 },
    { name: "metricDataFormat", type: "SHORT", value: 0 },
    { name: "numberOfHMetrics", type: "USHORT", value: 0 }
  ], options);
}
var hhea = { parse: parseHheaTable, make: makeHheaTable };
function parseHmtxTableAll(data, start, numMetrics, numGlyphs, glyphs) {
  var advanceWidth;
  var leftSideBearing;
  var p = new parse.Parser(data, start);
  for (var i = 0;i < numGlyphs; i += 1) {
    if (i < numMetrics) {
      advanceWidth = p.parseUShort();
      leftSideBearing = p.parseShort();
    }
    var glyph = glyphs.get(i);
    glyph.advanceWidth = advanceWidth;
    glyph.leftSideBearing = leftSideBearing;
  }
}
function parseHmtxTableOnLowMemory(font, data, start, numMetrics, numGlyphs) {
  font._hmtxTableData = {};
  var advanceWidth;
  var leftSideBearing;
  var p = new parse.Parser(data, start);
  for (var i = 0;i < numGlyphs; i += 1) {
    if (i < numMetrics) {
      advanceWidth = p.parseUShort();
      leftSideBearing = p.parseShort();
    }
    font._hmtxTableData[i] = {
      advanceWidth,
      leftSideBearing
    };
  }
}
function parseHmtxTable(font, data, start, numMetrics, numGlyphs, glyphs, opt) {
  if (opt.lowMemory) {
    parseHmtxTableOnLowMemory(font, data, start, numMetrics, numGlyphs);
  } else {
    parseHmtxTableAll(data, start, numMetrics, numGlyphs, glyphs);
  }
}
function makeHmtxTable(glyphs) {
  var t = new table.Table("hmtx", []);
  for (var i = 0;i < glyphs.length; i += 1) {
    var glyph = glyphs.get(i);
    var advanceWidth = glyph.advanceWidth || 0;
    var leftSideBearing = glyph.leftSideBearing || 0;
    t.fields.push({ name: "advanceWidth_" + i, type: "USHORT", value: advanceWidth });
    t.fields.push({ name: "leftSideBearing_" + i, type: "SHORT", value: leftSideBearing });
  }
  return t;
}
var hmtx = { parse: parseHmtxTable, make: makeHmtxTable };
function makeLtagTable(tags) {
  var result = new table.Table("ltag", [
    { name: "version", type: "ULONG", value: 1 },
    { name: "flags", type: "ULONG", value: 0 },
    { name: "numTags", type: "ULONG", value: tags.length }
  ]);
  var stringPool = "";
  var stringPoolOffset = 12 + tags.length * 4;
  for (var i = 0;i < tags.length; ++i) {
    var pos = stringPool.indexOf(tags[i]);
    if (pos < 0) {
      pos = stringPool.length;
      stringPool += tags[i];
    }
    result.fields.push({ name: "offset " + i, type: "USHORT", value: stringPoolOffset + pos });
    result.fields.push({ name: "length " + i, type: "USHORT", value: tags[i].length });
  }
  result.fields.push({ name: "stringPool", type: "CHARARRAY", value: stringPool });
  return result;
}
function parseLtagTable(data, start) {
  var p = new parse.Parser(data, start);
  var tableVersion = p.parseULong();
  check.argument(tableVersion === 1, "Unsupported ltag table version.");
  p.skip("uLong", 1);
  var numTags = p.parseULong();
  var tags = [];
  for (var i = 0;i < numTags; i++) {
    var tag = "";
    var offset2 = start + p.parseUShort();
    var length = p.parseUShort();
    for (var j = offset2;j < offset2 + length; ++j) {
      tag += String.fromCharCode(data.getInt8(j));
    }
    tags.push(tag);
  }
  return tags;
}
var ltag = { make: makeLtagTable, parse: parseLtagTable };
function parseMaxpTable(data, start) {
  var maxp2 = {};
  var p = new parse.Parser(data, start);
  maxp2.version = p.parseVersion();
  maxp2.numGlyphs = p.parseUShort();
  if (maxp2.version === 1) {
    maxp2.maxPoints = p.parseUShort();
    maxp2.maxContours = p.parseUShort();
    maxp2.maxCompositePoints = p.parseUShort();
    maxp2.maxCompositeContours = p.parseUShort();
    maxp2.maxZones = p.parseUShort();
    maxp2.maxTwilightPoints = p.parseUShort();
    maxp2.maxStorage = p.parseUShort();
    maxp2.maxFunctionDefs = p.parseUShort();
    maxp2.maxInstructionDefs = p.parseUShort();
    maxp2.maxStackElements = p.parseUShort();
    maxp2.maxSizeOfInstructions = p.parseUShort();
    maxp2.maxComponentElements = p.parseUShort();
    maxp2.maxComponentDepth = p.parseUShort();
  }
  return maxp2;
}
function makeMaxpTable(numGlyphs) {
  return new table.Table("maxp", [
    { name: "version", type: "FIXED", value: 20480 },
    { name: "numGlyphs", type: "USHORT", value: numGlyphs }
  ]);
}
var maxp = { parse: parseMaxpTable, make: makeMaxpTable };
var nameTableNames = [
  "copyright",
  "fontFamily",
  "fontSubfamily",
  "uniqueID",
  "fullName",
  "version",
  "postScriptName",
  "trademark",
  "manufacturer",
  "designer",
  "description",
  "manufacturerURL",
  "designerURL",
  "license",
  "licenseURL",
  "reserved",
  "preferredFamily",
  "preferredSubfamily",
  "compatibleFullName",
  "sampleText",
  "postScriptFindFontName",
  "wwsFamily",
  "wwsSubfamily"
];
var macLanguages = {
  0: "en",
  1: "fr",
  2: "de",
  3: "it",
  4: "nl",
  5: "sv",
  6: "es",
  7: "da",
  8: "pt",
  9: "no",
  10: "he",
  11: "ja",
  12: "ar",
  13: "fi",
  14: "el",
  15: "is",
  16: "mt",
  17: "tr",
  18: "hr",
  19: "zh-Hant",
  20: "ur",
  21: "hi",
  22: "th",
  23: "ko",
  24: "lt",
  25: "pl",
  26: "hu",
  27: "es",
  28: "lv",
  29: "se",
  30: "fo",
  31: "fa",
  32: "ru",
  33: "zh",
  34: "nl-BE",
  35: "ga",
  36: "sq",
  37: "ro",
  38: "cz",
  39: "sk",
  40: "si",
  41: "yi",
  42: "sr",
  43: "mk",
  44: "bg",
  45: "uk",
  46: "be",
  47: "uz",
  48: "kk",
  49: "az-Cyrl",
  50: "az-Arab",
  51: "hy",
  52: "ka",
  53: "mo",
  54: "ky",
  55: "tg",
  56: "tk",
  57: "mn-CN",
  58: "mn",
  59: "ps",
  60: "ks",
  61: "ku",
  62: "sd",
  63: "bo",
  64: "ne",
  65: "sa",
  66: "mr",
  67: "bn",
  68: "as",
  69: "gu",
  70: "pa",
  71: "or",
  72: "ml",
  73: "kn",
  74: "ta",
  75: "te",
  76: "si",
  77: "my",
  78: "km",
  79: "lo",
  80: "vi",
  81: "id",
  82: "tl",
  83: "ms",
  84: "ms-Arab",
  85: "am",
  86: "ti",
  87: "om",
  88: "so",
  89: "sw",
  90: "rw",
  91: "rn",
  92: "ny",
  93: "mg",
  94: "eo",
  128: "cy",
  129: "eu",
  130: "ca",
  131: "la",
  132: "qu",
  133: "gn",
  134: "ay",
  135: "tt",
  136: "ug",
  137: "dz",
  138: "jv",
  139: "su",
  140: "gl",
  141: "af",
  142: "br",
  143: "iu",
  144: "gd",
  145: "gv",
  146: "ga",
  147: "to",
  148: "el-polyton",
  149: "kl",
  150: "az",
  151: "nn"
};
var macLanguageToScript = {
  0: 0,
  1: 0,
  2: 0,
  3: 0,
  4: 0,
  5: 0,
  6: 0,
  7: 0,
  8: 0,
  9: 0,
  10: 5,
  11: 1,
  12: 4,
  13: 0,
  14: 6,
  15: 0,
  16: 0,
  17: 0,
  18: 0,
  19: 2,
  20: 4,
  21: 9,
  22: 21,
  23: 3,
  24: 29,
  25: 29,
  26: 29,
  27: 29,
  28: 29,
  29: 0,
  30: 0,
  31: 4,
  32: 7,
  33: 25,
  34: 0,
  35: 0,
  36: 0,
  37: 0,
  38: 29,
  39: 29,
  40: 0,
  41: 5,
  42: 7,
  43: 7,
  44: 7,
  45: 7,
  46: 7,
  47: 7,
  48: 7,
  49: 7,
  50: 4,
  51: 24,
  52: 23,
  53: 7,
  54: 7,
  55: 7,
  56: 7,
  57: 27,
  58: 7,
  59: 4,
  60: 4,
  61: 4,
  62: 4,
  63: 26,
  64: 9,
  65: 9,
  66: 9,
  67: 13,
  68: 13,
  69: 11,
  70: 10,
  71: 12,
  72: 17,
  73: 16,
  74: 14,
  75: 15,
  76: 18,
  77: 19,
  78: 20,
  79: 22,
  80: 30,
  81: 0,
  82: 0,
  83: 0,
  84: 4,
  85: 28,
  86: 28,
  87: 28,
  88: 0,
  89: 0,
  90: 0,
  91: 0,
  92: 0,
  93: 0,
  94: 0,
  128: 0,
  129: 0,
  130: 0,
  131: 0,
  132: 0,
  133: 0,
  134: 0,
  135: 7,
  136: 4,
  137: 26,
  138: 0,
  139: 0,
  140: 0,
  141: 0,
  142: 0,
  143: 28,
  144: 0,
  145: 0,
  146: 0,
  147: 0,
  148: 6,
  149: 0,
  150: 0,
  151: 0
};
var windowsLanguages = {
  1078: "af",
  1052: "sq",
  1156: "gsw",
  1118: "am",
  5121: "ar-DZ",
  15361: "ar-BH",
  3073: "ar",
  2049: "ar-IQ",
  11265: "ar-JO",
  13313: "ar-KW",
  12289: "ar-LB",
  4097: "ar-LY",
  6145: "ary",
  8193: "ar-OM",
  16385: "ar-QA",
  1025: "ar-SA",
  10241: "ar-SY",
  7169: "aeb",
  14337: "ar-AE",
  9217: "ar-YE",
  1067: "hy",
  1101: "as",
  2092: "az-Cyrl",
  1068: "az",
  1133: "ba",
  1069: "eu",
  1059: "be",
  2117: "bn",
  1093: "bn-IN",
  8218: "bs-Cyrl",
  5146: "bs",
  1150: "br",
  1026: "bg",
  1027: "ca",
  3076: "zh-HK",
  5124: "zh-MO",
  2052: "zh",
  4100: "zh-SG",
  1028: "zh-TW",
  1155: "co",
  1050: "hr",
  4122: "hr-BA",
  1029: "cs",
  1030: "da",
  1164: "prs",
  1125: "dv",
  2067: "nl-BE",
  1043: "nl",
  3081: "en-AU",
  10249: "en-BZ",
  4105: "en-CA",
  9225: "en-029",
  16393: "en-IN",
  6153: "en-IE",
  8201: "en-JM",
  17417: "en-MY",
  5129: "en-NZ",
  13321: "en-PH",
  18441: "en-SG",
  7177: "en-ZA",
  11273: "en-TT",
  2057: "en-GB",
  1033: "en",
  12297: "en-ZW",
  1061: "et",
  1080: "fo",
  1124: "fil",
  1035: "fi",
  2060: "fr-BE",
  3084: "fr-CA",
  1036: "fr",
  5132: "fr-LU",
  6156: "fr-MC",
  4108: "fr-CH",
  1122: "fy",
  1110: "gl",
  1079: "ka",
  3079: "de-AT",
  1031: "de",
  5127: "de-LI",
  4103: "de-LU",
  2055: "de-CH",
  1032: "el",
  1135: "kl",
  1095: "gu",
  1128: "ha",
  1037: "he",
  1081: "hi",
  1038: "hu",
  1039: "is",
  1136: "ig",
  1057: "id",
  1117: "iu",
  2141: "iu-Latn",
  2108: "ga",
  1076: "xh",
  1077: "zu",
  1040: "it",
  2064: "it-CH",
  1041: "ja",
  1099: "kn",
  1087: "kk",
  1107: "km",
  1158: "quc",
  1159: "rw",
  1089: "sw",
  1111: "kok",
  1042: "ko",
  1088: "ky",
  1108: "lo",
  1062: "lv",
  1063: "lt",
  2094: "dsb",
  1134: "lb",
  1071: "mk",
  2110: "ms-BN",
  1086: "ms",
  1100: "ml",
  1082: "mt",
  1153: "mi",
  1146: "arn",
  1102: "mr",
  1148: "moh",
  1104: "mn",
  2128: "mn-CN",
  1121: "ne",
  1044: "nb",
  2068: "nn",
  1154: "oc",
  1096: "or",
  1123: "ps",
  1045: "pl",
  1046: "pt",
  2070: "pt-PT",
  1094: "pa",
  1131: "qu-BO",
  2155: "qu-EC",
  3179: "qu",
  1048: "ro",
  1047: "rm",
  1049: "ru",
  9275: "smn",
  4155: "smj-NO",
  5179: "smj",
  3131: "se-FI",
  1083: "se",
  2107: "se-SE",
  8251: "sms",
  6203: "sma-NO",
  7227: "sms",
  1103: "sa",
  7194: "sr-Cyrl-BA",
  3098: "sr",
  6170: "sr-Latn-BA",
  2074: "sr-Latn",
  1132: "nso",
  1074: "tn",
  1115: "si",
  1051: "sk",
  1060: "sl",
  11274: "es-AR",
  16394: "es-BO",
  13322: "es-CL",
  9226: "es-CO",
  5130: "es-CR",
  7178: "es-DO",
  12298: "es-EC",
  17418: "es-SV",
  4106: "es-GT",
  18442: "es-HN",
  2058: "es-MX",
  19466: "es-NI",
  6154: "es-PA",
  15370: "es-PY",
  10250: "es-PE",
  20490: "es-PR",
  3082: "es",
  1034: "es",
  21514: "es-US",
  14346: "es-UY",
  8202: "es-VE",
  2077: "sv-FI",
  1053: "sv",
  1114: "syr",
  1064: "tg",
  2143: "tzm",
  1097: "ta",
  1092: "tt",
  1098: "te",
  1054: "th",
  1105: "bo",
  1055: "tr",
  1090: "tk",
  1152: "ug",
  1058: "uk",
  1070: "hsb",
  1056: "ur",
  2115: "uz-Cyrl",
  1091: "uz",
  1066: "vi",
  1106: "cy",
  1160: "wo",
  1157: "sah",
  1144: "ii",
  1130: "yo"
};
function getLanguageCode(platformID, languageID, ltag2) {
  switch (platformID) {
    case 0:
      if (languageID === 65535) {
        return "und";
      } else if (ltag2) {
        return ltag2[languageID];
      }
      break;
    case 1:
      return macLanguages[languageID];
    case 3:
      return windowsLanguages[languageID];
  }
  return;
}
var utf16 = "utf-16";
var macScriptEncodings = {
  0: "macintosh",
  1: "x-mac-japanese",
  2: "x-mac-chinesetrad",
  3: "x-mac-korean",
  6: "x-mac-greek",
  7: "x-mac-cyrillic",
  9: "x-mac-devanagai",
  10: "x-mac-gurmukhi",
  11: "x-mac-gujarati",
  12: "x-mac-oriya",
  13: "x-mac-bengali",
  14: "x-mac-tamil",
  15: "x-mac-telugu",
  16: "x-mac-kannada",
  17: "x-mac-malayalam",
  18: "x-mac-sinhalese",
  19: "x-mac-burmese",
  20: "x-mac-khmer",
  21: "x-mac-thai",
  22: "x-mac-lao",
  23: "x-mac-georgian",
  24: "x-mac-armenian",
  25: "x-mac-chinesesimp",
  26: "x-mac-tibetan",
  27: "x-mac-mongolian",
  28: "x-mac-ethiopic",
  29: "x-mac-ce",
  30: "x-mac-vietnamese",
  31: "x-mac-extarabic"
};
var macLanguageEncodings = {
  15: "x-mac-icelandic",
  17: "x-mac-turkish",
  18: "x-mac-croatian",
  24: "x-mac-ce",
  25: "x-mac-ce",
  26: "x-mac-ce",
  27: "x-mac-ce",
  28: "x-mac-ce",
  30: "x-mac-icelandic",
  37: "x-mac-romanian",
  38: "x-mac-ce",
  39: "x-mac-ce",
  40: "x-mac-ce",
  143: "x-mac-inuit",
  146: "x-mac-gaelic"
};
function getEncoding(platformID, encodingID, languageID) {
  switch (platformID) {
    case 0:
      return utf16;
    case 1:
      return macLanguageEncodings[languageID] || macScriptEncodings[encodingID];
    case 3:
      if (encodingID === 1 || encodingID === 10) {
        return utf16;
      }
      break;
  }
  return;
}
function parseNameTable(data, start, ltag2) {
  var name = {};
  var p = new parse.Parser(data, start);
  var format = p.parseUShort();
  var count = p.parseUShort();
  var stringOffset = p.offset + p.parseUShort();
  for (var i = 0;i < count; i++) {
    var platformID = p.parseUShort();
    var encodingID = p.parseUShort();
    var languageID = p.parseUShort();
    var nameID = p.parseUShort();
    var property = nameTableNames[nameID] || nameID;
    var byteLength = p.parseUShort();
    var offset2 = p.parseUShort();
    var language = getLanguageCode(platformID, languageID, ltag2);
    var encoding = getEncoding(platformID, encodingID, languageID);
    if (encoding !== undefined && language !== undefined) {
      var text = undefined;
      if (encoding === utf16) {
        text = decode.UTF16(data, stringOffset + offset2, byteLength);
      } else {
        text = decode.MACSTRING(data, stringOffset + offset2, byteLength, encoding);
      }
      if (text) {
        var translations = name[property];
        if (translations === undefined) {
          translations = name[property] = {};
        }
        translations[language] = text;
      }
    }
  }
  if (format === 1) {
    p.parseUShort();
  }
  return name;
}
function reverseDict(dict) {
  var result = {};
  for (var key in dict) {
    result[dict[key]] = parseInt(key);
  }
  return result;
}
function makeNameRecord(platformID, encodingID, languageID, nameID, length, offset2) {
  return new table.Record("NameRecord", [
    { name: "platformID", type: "USHORT", value: platformID },
    { name: "encodingID", type: "USHORT", value: encodingID },
    { name: "languageID", type: "USHORT", value: languageID },
    { name: "nameID", type: "USHORT", value: nameID },
    { name: "length", type: "USHORT", value: length },
    { name: "offset", type: "USHORT", value: offset2 }
  ]);
}
function findSubArray(needle, haystack) {
  var needleLength = needle.length;
  var limit = haystack.length - needleLength + 1;
  loop:
    for (var pos = 0;pos < limit; pos++) {
      for (;pos < limit; pos++) {
        for (var k = 0;k < needleLength; k++) {
          if (haystack[pos + k] !== needle[k]) {
            continue loop;
          }
        }
        return pos;
      }
    }
  return -1;
}
function addStringToPool(s, pool) {
  var offset2 = findSubArray(s, pool);
  if (offset2 < 0) {
    offset2 = pool.length;
    var i = 0;
    var len = s.length;
    for (;i < len; ++i) {
      pool.push(s[i]);
    }
  }
  return offset2;
}
function makeNameTable(names, ltag2) {
  var nameID;
  var nameIDs = [];
  var namesWithNumericKeys = {};
  var nameTableIds = reverseDict(nameTableNames);
  for (var key in names) {
    var id = nameTableIds[key];
    if (id === undefined) {
      id = key;
    }
    nameID = parseInt(id);
    if (isNaN(nameID)) {
      throw new Error('Name table entry "' + key + '" does not exist, see nameTableNames for complete list.');
    }
    namesWithNumericKeys[nameID] = names[key];
    nameIDs.push(nameID);
  }
  var macLanguageIds = reverseDict(macLanguages);
  var windowsLanguageIds = reverseDict(windowsLanguages);
  var nameRecords = [];
  var stringPool = [];
  for (var i = 0;i < nameIDs.length; i++) {
    nameID = nameIDs[i];
    var translations = namesWithNumericKeys[nameID];
    for (var lang in translations) {
      var text = translations[lang];
      var macPlatform = 1;
      var macLanguage = macLanguageIds[lang];
      var macScript = macLanguageToScript[macLanguage];
      var macEncoding = getEncoding(macPlatform, macScript, macLanguage);
      var macName = encode.MACSTRING(text, macEncoding);
      if (macName === undefined) {
        macPlatform = 0;
        macLanguage = ltag2.indexOf(lang);
        if (macLanguage < 0) {
          macLanguage = ltag2.length;
          ltag2.push(lang);
        }
        macScript = 4;
        macName = encode.UTF16(text);
      }
      var macNameOffset = addStringToPool(macName, stringPool);
      nameRecords.push(makeNameRecord(macPlatform, macScript, macLanguage, nameID, macName.length, macNameOffset));
      var winLanguage = windowsLanguageIds[lang];
      if (winLanguage !== undefined) {
        var winName = encode.UTF16(text);
        var winNameOffset = addStringToPool(winName, stringPool);
        nameRecords.push(makeNameRecord(3, 1, winLanguage, nameID, winName.length, winNameOffset));
      }
    }
  }
  nameRecords.sort(function(a, b) {
    return a.platformID - b.platformID || a.encodingID - b.encodingID || a.languageID - b.languageID || a.nameID - b.nameID;
  });
  var t = new table.Table("name", [
    { name: "format", type: "USHORT", value: 0 },
    { name: "count", type: "USHORT", value: nameRecords.length },
    { name: "stringOffset", type: "USHORT", value: 6 + nameRecords.length * 12 }
  ]);
  for (var r = 0;r < nameRecords.length; r++) {
    t.fields.push({ name: "record_" + r, type: "RECORD", value: nameRecords[r] });
  }
  t.fields.push({ name: "strings", type: "LITERAL", value: stringPool });
  return t;
}
var _name = { parse: parseNameTable, make: makeNameTable };
var unicodeRanges = [
  { begin: 0, end: 127 },
  { begin: 128, end: 255 },
  { begin: 256, end: 383 },
  { begin: 384, end: 591 },
  { begin: 592, end: 687 },
  { begin: 688, end: 767 },
  { begin: 768, end: 879 },
  { begin: 880, end: 1023 },
  { begin: 11392, end: 11519 },
  { begin: 1024, end: 1279 },
  { begin: 1328, end: 1423 },
  { begin: 1424, end: 1535 },
  { begin: 42240, end: 42559 },
  { begin: 1536, end: 1791 },
  { begin: 1984, end: 2047 },
  { begin: 2304, end: 2431 },
  { begin: 2432, end: 2559 },
  { begin: 2560, end: 2687 },
  { begin: 2688, end: 2815 },
  { begin: 2816, end: 2943 },
  { begin: 2944, end: 3071 },
  { begin: 3072, end: 3199 },
  { begin: 3200, end: 3327 },
  { begin: 3328, end: 3455 },
  { begin: 3584, end: 3711 },
  { begin: 3712, end: 3839 },
  { begin: 4256, end: 4351 },
  { begin: 6912, end: 7039 },
  { begin: 4352, end: 4607 },
  { begin: 7680, end: 7935 },
  { begin: 7936, end: 8191 },
  { begin: 8192, end: 8303 },
  { begin: 8304, end: 8351 },
  { begin: 8352, end: 8399 },
  { begin: 8400, end: 8447 },
  { begin: 8448, end: 8527 },
  { begin: 8528, end: 8591 },
  { begin: 8592, end: 8703 },
  { begin: 8704, end: 8959 },
  { begin: 8960, end: 9215 },
  { begin: 9216, end: 9279 },
  { begin: 9280, end: 9311 },
  { begin: 9312, end: 9471 },
  { begin: 9472, end: 9599 },
  { begin: 9600, end: 9631 },
  { begin: 9632, end: 9727 },
  { begin: 9728, end: 9983 },
  { begin: 9984, end: 10175 },
  { begin: 12288, end: 12351 },
  { begin: 12352, end: 12447 },
  { begin: 12448, end: 12543 },
  { begin: 12544, end: 12591 },
  { begin: 12592, end: 12687 },
  { begin: 43072, end: 43135 },
  { begin: 12800, end: 13055 },
  { begin: 13056, end: 13311 },
  { begin: 44032, end: 55215 },
  { begin: 55296, end: 57343 },
  { begin: 67840, end: 67871 },
  { begin: 19968, end: 40959 },
  { begin: 57344, end: 63743 },
  { begin: 12736, end: 12783 },
  { begin: 64256, end: 64335 },
  { begin: 64336, end: 65023 },
  { begin: 65056, end: 65071 },
  { begin: 65040, end: 65055 },
  { begin: 65104, end: 65135 },
  { begin: 65136, end: 65279 },
  { begin: 65280, end: 65519 },
  { begin: 65520, end: 65535 },
  { begin: 3840, end: 4095 },
  { begin: 1792, end: 1871 },
  { begin: 1920, end: 1983 },
  { begin: 3456, end: 3583 },
  { begin: 4096, end: 4255 },
  { begin: 4608, end: 4991 },
  { begin: 5024, end: 5119 },
  { begin: 5120, end: 5759 },
  { begin: 5760, end: 5791 },
  { begin: 5792, end: 5887 },
  { begin: 6016, end: 6143 },
  { begin: 6144, end: 6319 },
  { begin: 10240, end: 10495 },
  { begin: 40960, end: 42127 },
  { begin: 5888, end: 5919 },
  { begin: 66304, end: 66351 },
  { begin: 66352, end: 66383 },
  { begin: 66560, end: 66639 },
  { begin: 118784, end: 119039 },
  { begin: 119808, end: 120831 },
  { begin: 1044480, end: 1048573 },
  { begin: 65024, end: 65039 },
  { begin: 917504, end: 917631 },
  { begin: 6400, end: 6479 },
  { begin: 6480, end: 6527 },
  { begin: 6528, end: 6623 },
  { begin: 6656, end: 6687 },
  { begin: 11264, end: 11359 },
  { begin: 11568, end: 11647 },
  { begin: 19904, end: 19967 },
  { begin: 43008, end: 43055 },
  { begin: 65536, end: 65663 },
  { begin: 65856, end: 65935 },
  { begin: 66432, end: 66463 },
  { begin: 66464, end: 66527 },
  { begin: 66640, end: 66687 },
  { begin: 66688, end: 66735 },
  { begin: 67584, end: 67647 },
  { begin: 68096, end: 68191 },
  { begin: 119552, end: 119647 },
  { begin: 73728, end: 74751 },
  { begin: 119648, end: 119679 },
  { begin: 7040, end: 7103 },
  { begin: 7168, end: 7247 },
  { begin: 7248, end: 7295 },
  { begin: 43136, end: 43231 },
  { begin: 43264, end: 43311 },
  { begin: 43312, end: 43359 },
  { begin: 43520, end: 43615 },
  { begin: 65936, end: 65999 },
  { begin: 66000, end: 66047 },
  { begin: 66208, end: 66271 },
  { begin: 127024, end: 127135 }
];
function getUnicodeRange(unicode) {
  for (var i = 0;i < unicodeRanges.length; i += 1) {
    var range2 = unicodeRanges[i];
    if (unicode >= range2.begin && unicode < range2.end) {
      return i;
    }
  }
  return -1;
}
function parseOS2Table(data, start) {
  var os22 = {};
  var p = new parse.Parser(data, start);
  os22.version = p.parseUShort();
  os22.xAvgCharWidth = p.parseShort();
  os22.usWeightClass = p.parseUShort();
  os22.usWidthClass = p.parseUShort();
  os22.fsType = p.parseUShort();
  os22.ySubscriptXSize = p.parseShort();
  os22.ySubscriptYSize = p.parseShort();
  os22.ySubscriptXOffset = p.parseShort();
  os22.ySubscriptYOffset = p.parseShort();
  os22.ySuperscriptXSize = p.parseShort();
  os22.ySuperscriptYSize = p.parseShort();
  os22.ySuperscriptXOffset = p.parseShort();
  os22.ySuperscriptYOffset = p.parseShort();
  os22.yStrikeoutSize = p.parseShort();
  os22.yStrikeoutPosition = p.parseShort();
  os22.sFamilyClass = p.parseShort();
  os22.panose = [];
  for (var i = 0;i < 10; i++) {
    os22.panose[i] = p.parseByte();
  }
  os22.ulUnicodeRange1 = p.parseULong();
  os22.ulUnicodeRange2 = p.parseULong();
  os22.ulUnicodeRange3 = p.parseULong();
  os22.ulUnicodeRange4 = p.parseULong();
  os22.achVendID = String.fromCharCode(p.parseByte(), p.parseByte(), p.parseByte(), p.parseByte());
  os22.fsSelection = p.parseUShort();
  os22.usFirstCharIndex = p.parseUShort();
  os22.usLastCharIndex = p.parseUShort();
  os22.sTypoAscender = p.parseShort();
  os22.sTypoDescender = p.parseShort();
  os22.sTypoLineGap = p.parseShort();
  os22.usWinAscent = p.parseUShort();
  os22.usWinDescent = p.parseUShort();
  if (os22.version >= 1) {
    os22.ulCodePageRange1 = p.parseULong();
    os22.ulCodePageRange2 = p.parseULong();
  }
  if (os22.version >= 2) {
    os22.sxHeight = p.parseShort();
    os22.sCapHeight = p.parseShort();
    os22.usDefaultChar = p.parseUShort();
    os22.usBreakChar = p.parseUShort();
    os22.usMaxContent = p.parseUShort();
  }
  return os22;
}
function makeOS2Table(options) {
  return new table.Table("OS/2", [
    { name: "version", type: "USHORT", value: 3 },
    { name: "xAvgCharWidth", type: "SHORT", value: 0 },
    { name: "usWeightClass", type: "USHORT", value: 0 },
    { name: "usWidthClass", type: "USHORT", value: 0 },
    { name: "fsType", type: "USHORT", value: 0 },
    { name: "ySubscriptXSize", type: "SHORT", value: 650 },
    { name: "ySubscriptYSize", type: "SHORT", value: 699 },
    { name: "ySubscriptXOffset", type: "SHORT", value: 0 },
    { name: "ySubscriptYOffset", type: "SHORT", value: 140 },
    { name: "ySuperscriptXSize", type: "SHORT", value: 650 },
    { name: "ySuperscriptYSize", type: "SHORT", value: 699 },
    { name: "ySuperscriptXOffset", type: "SHORT", value: 0 },
    { name: "ySuperscriptYOffset", type: "SHORT", value: 479 },
    { name: "yStrikeoutSize", type: "SHORT", value: 49 },
    { name: "yStrikeoutPosition", type: "SHORT", value: 258 },
    { name: "sFamilyClass", type: "SHORT", value: 0 },
    { name: "bFamilyType", type: "BYTE", value: 0 },
    { name: "bSerifStyle", type: "BYTE", value: 0 },
    { name: "bWeight", type: "BYTE", value: 0 },
    { name: "bProportion", type: "BYTE", value: 0 },
    { name: "bContrast", type: "BYTE", value: 0 },
    { name: "bStrokeVariation", type: "BYTE", value: 0 },
    { name: "bArmStyle", type: "BYTE", value: 0 },
    { name: "bLetterform", type: "BYTE", value: 0 },
    { name: "bMidline", type: "BYTE", value: 0 },
    { name: "bXHeight", type: "BYTE", value: 0 },
    { name: "ulUnicodeRange1", type: "ULONG", value: 0 },
    { name: "ulUnicodeRange2", type: "ULONG", value: 0 },
    { name: "ulUnicodeRange3", type: "ULONG", value: 0 },
    { name: "ulUnicodeRange4", type: "ULONG", value: 0 },
    { name: "achVendID", type: "CHARARRAY", value: "XXXX" },
    { name: "fsSelection", type: "USHORT", value: 0 },
    { name: "usFirstCharIndex", type: "USHORT", value: 0 },
    { name: "usLastCharIndex", type: "USHORT", value: 0 },
    { name: "sTypoAscender", type: "SHORT", value: 0 },
    { name: "sTypoDescender", type: "SHORT", value: 0 },
    { name: "sTypoLineGap", type: "SHORT", value: 0 },
    { name: "usWinAscent", type: "USHORT", value: 0 },
    { name: "usWinDescent", type: "USHORT", value: 0 },
    { name: "ulCodePageRange1", type: "ULONG", value: 0 },
    { name: "ulCodePageRange2", type: "ULONG", value: 0 },
    { name: "sxHeight", type: "SHORT", value: 0 },
    { name: "sCapHeight", type: "SHORT", value: 0 },
    { name: "usDefaultChar", type: "USHORT", value: 0 },
    { name: "usBreakChar", type: "USHORT", value: 0 },
    { name: "usMaxContext", type: "USHORT", value: 0 }
  ], options);
}
var os2 = { parse: parseOS2Table, make: makeOS2Table, unicodeRanges, getUnicodeRange };
function parsePostTable(data, start) {
  var post2 = {};
  var p = new parse.Parser(data, start);
  post2.version = p.parseVersion();
  post2.italicAngle = p.parseFixed();
  post2.underlinePosition = p.parseShort();
  post2.underlineThickness = p.parseShort();
  post2.isFixedPitch = p.parseULong();
  post2.minMemType42 = p.parseULong();
  post2.maxMemType42 = p.parseULong();
  post2.minMemType1 = p.parseULong();
  post2.maxMemType1 = p.parseULong();
  switch (post2.version) {
    case 1:
      post2.names = standardNames.slice();
      break;
    case 2:
      post2.numberOfGlyphs = p.parseUShort();
      post2.glyphNameIndex = new Array(post2.numberOfGlyphs);
      for (var i = 0;i < post2.numberOfGlyphs; i++) {
        post2.glyphNameIndex[i] = p.parseUShort();
      }
      post2.names = [];
      for (var i$1 = 0;i$1 < post2.numberOfGlyphs; i$1++) {
        if (post2.glyphNameIndex[i$1] >= standardNames.length) {
          var nameLength = p.parseChar();
          post2.names.push(p.parseString(nameLength));
        }
      }
      break;
    case 2.5:
      post2.numberOfGlyphs = p.parseUShort();
      post2.offset = new Array(post2.numberOfGlyphs);
      for (var i$2 = 0;i$2 < post2.numberOfGlyphs; i$2++) {
        post2.offset[i$2] = p.parseChar();
      }
      break;
  }
  return post2;
}
function makePostTable() {
  return new table.Table("post", [
    { name: "version", type: "FIXED", value: 196608 },
    { name: "italicAngle", type: "FIXED", value: 0 },
    { name: "underlinePosition", type: "FWORD", value: 0 },
    { name: "underlineThickness", type: "FWORD", value: 0 },
    { name: "isFixedPitch", type: "ULONG", value: 0 },
    { name: "minMemType42", type: "ULONG", value: 0 },
    { name: "maxMemType42", type: "ULONG", value: 0 },
    { name: "minMemType1", type: "ULONG", value: 0 },
    { name: "maxMemType1", type: "ULONG", value: 0 }
  ]);
}
var post = { parse: parsePostTable, make: makePostTable };
var subtableParsers = new Array(9);
subtableParsers[1] = function parseLookup1() {
  var start = this.offset + this.relativeOffset;
  var substFormat = this.parseUShort();
  if (substFormat === 1) {
    return {
      substFormat: 1,
      coverage: this.parsePointer(Parser.coverage),
      deltaGlyphId: this.parseUShort()
    };
  } else if (substFormat === 2) {
    return {
      substFormat: 2,
      coverage: this.parsePointer(Parser.coverage),
      substitute: this.parseOffset16List()
    };
  }
  check.assert(false, "0x" + start.toString(16) + ": lookup type 1 format must be 1 or 2.");
};
subtableParsers[2] = function parseLookup2() {
  var substFormat = this.parseUShort();
  check.argument(substFormat === 1, "GSUB Multiple Substitution Subtable identifier-format must be 1");
  return {
    substFormat,
    coverage: this.parsePointer(Parser.coverage),
    sequences: this.parseListOfLists()
  };
};
subtableParsers[3] = function parseLookup3() {
  var substFormat = this.parseUShort();
  check.argument(substFormat === 1, "GSUB Alternate Substitution Subtable identifier-format must be 1");
  return {
    substFormat,
    coverage: this.parsePointer(Parser.coverage),
    alternateSets: this.parseListOfLists()
  };
};
subtableParsers[4] = function parseLookup4() {
  var substFormat = this.parseUShort();
  check.argument(substFormat === 1, "GSUB ligature table identifier-format must be 1");
  return {
    substFormat,
    coverage: this.parsePointer(Parser.coverage),
    ligatureSets: this.parseListOfLists(function() {
      return {
        ligGlyph: this.parseUShort(),
        components: this.parseUShortList(this.parseUShort() - 1)
      };
    })
  };
};
var lookupRecordDesc = {
  sequenceIndex: Parser.uShort,
  lookupListIndex: Parser.uShort
};
subtableParsers[5] = function parseLookup5() {
  var start = this.offset + this.relativeOffset;
  var substFormat = this.parseUShort();
  if (substFormat === 1) {
    return {
      substFormat,
      coverage: this.parsePointer(Parser.coverage),
      ruleSets: this.parseListOfLists(function() {
        var glyphCount2 = this.parseUShort();
        var substCount2 = this.parseUShort();
        return {
          input: this.parseUShortList(glyphCount2 - 1),
          lookupRecords: this.parseRecordList(substCount2, lookupRecordDesc)
        };
      })
    };
  } else if (substFormat === 2) {
    return {
      substFormat,
      coverage: this.parsePointer(Parser.coverage),
      classDef: this.parsePointer(Parser.classDef),
      classSets: this.parseListOfLists(function() {
        var glyphCount2 = this.parseUShort();
        var substCount2 = this.parseUShort();
        return {
          classes: this.parseUShortList(glyphCount2 - 1),
          lookupRecords: this.parseRecordList(substCount2, lookupRecordDesc)
        };
      })
    };
  } else if (substFormat === 3) {
    var glyphCount = this.parseUShort();
    var substCount = this.parseUShort();
    return {
      substFormat,
      coverages: this.parseList(glyphCount, Parser.pointer(Parser.coverage)),
      lookupRecords: this.parseRecordList(substCount, lookupRecordDesc)
    };
  }
  check.assert(false, "0x" + start.toString(16) + ": lookup type 5 format must be 1, 2 or 3.");
};
subtableParsers[6] = function parseLookup6() {
  var start = this.offset + this.relativeOffset;
  var substFormat = this.parseUShort();
  if (substFormat === 1) {
    return {
      substFormat: 1,
      coverage: this.parsePointer(Parser.coverage),
      chainRuleSets: this.parseListOfLists(function() {
        return {
          backtrack: this.parseUShortList(),
          input: this.parseUShortList(this.parseShort() - 1),
          lookahead: this.parseUShortList(),
          lookupRecords: this.parseRecordList(lookupRecordDesc)
        };
      })
    };
  } else if (substFormat === 2) {
    return {
      substFormat: 2,
      coverage: this.parsePointer(Parser.coverage),
      backtrackClassDef: this.parsePointer(Parser.classDef),
      inputClassDef: this.parsePointer(Parser.classDef),
      lookaheadClassDef: this.parsePointer(Parser.classDef),
      chainClassSet: this.parseListOfLists(function() {
        return {
          backtrack: this.parseUShortList(),
          input: this.parseUShortList(this.parseShort() - 1),
          lookahead: this.parseUShortList(),
          lookupRecords: this.parseRecordList(lookupRecordDesc)
        };
      })
    };
  } else if (substFormat === 3) {
    return {
      substFormat: 3,
      backtrackCoverage: this.parseList(Parser.pointer(Parser.coverage)),
      inputCoverage: this.parseList(Parser.pointer(Parser.coverage)),
      lookaheadCoverage: this.parseList(Parser.pointer(Parser.coverage)),
      lookupRecords: this.parseRecordList(lookupRecordDesc)
    };
  }
  check.assert(false, "0x" + start.toString(16) + ": lookup type 6 format must be 1, 2 or 3.");
};
subtableParsers[7] = function parseLookup7() {
  var substFormat = this.parseUShort();
  check.argument(substFormat === 1, "GSUB Extension Substitution subtable identifier-format must be 1");
  var extensionLookupType = this.parseUShort();
  var extensionParser = new Parser(this.data, this.offset + this.parseULong());
  return {
    substFormat: 1,
    lookupType: extensionLookupType,
    extension: subtableParsers[extensionLookupType].call(extensionParser)
  };
};
subtableParsers[8] = function parseLookup8() {
  var substFormat = this.parseUShort();
  check.argument(substFormat === 1, "GSUB Reverse Chaining Contextual Single Substitution Subtable identifier-format must be 1");
  return {
    substFormat,
    coverage: this.parsePointer(Parser.coverage),
    backtrackCoverage: this.parseList(Parser.pointer(Parser.coverage)),
    lookaheadCoverage: this.parseList(Parser.pointer(Parser.coverage)),
    substitutes: this.parseUShortList()
  };
};
function parseGsubTable(data, start) {
  start = start || 0;
  var p = new Parser(data, start);
  var tableVersion = p.parseVersion(1);
  check.argument(tableVersion === 1 || tableVersion === 1.1, "Unsupported GSUB table version.");
  if (tableVersion === 1) {
    return {
      version: tableVersion,
      scripts: p.parseScriptList(),
      features: p.parseFeatureList(),
      lookups: p.parseLookupList(subtableParsers)
    };
  } else {
    return {
      version: tableVersion,
      scripts: p.parseScriptList(),
      features: p.parseFeatureList(),
      lookups: p.parseLookupList(subtableParsers),
      variations: p.parseFeatureVariationsList()
    };
  }
}
var subtableMakers = new Array(9);
subtableMakers[1] = function makeLookup1(subtable) {
  if (subtable.substFormat === 1) {
    return new table.Table("substitutionTable", [
      { name: "substFormat", type: "USHORT", value: 1 },
      { name: "coverage", type: "TABLE", value: new table.Coverage(subtable.coverage) },
      { name: "deltaGlyphID", type: "USHORT", value: subtable.deltaGlyphId }
    ]);
  } else {
    return new table.Table("substitutionTable", [
      { name: "substFormat", type: "USHORT", value: 2 },
      { name: "coverage", type: "TABLE", value: new table.Coverage(subtable.coverage) }
    ].concat(table.ushortList("substitute", subtable.substitute)));
  }
};
subtableMakers[2] = function makeLookup2(subtable) {
  check.assert(subtable.substFormat === 1, "Lookup type 2 substFormat must be 1.");
  return new table.Table("substitutionTable", [
    { name: "substFormat", type: "USHORT", value: 1 },
    { name: "coverage", type: "TABLE", value: new table.Coverage(subtable.coverage) }
  ].concat(table.tableList("seqSet", subtable.sequences, function(sequenceSet) {
    return new table.Table("sequenceSetTable", table.ushortList("sequence", sequenceSet));
  })));
};
subtableMakers[3] = function makeLookup3(subtable) {
  check.assert(subtable.substFormat === 1, "Lookup type 3 substFormat must be 1.");
  return new table.Table("substitutionTable", [
    { name: "substFormat", type: "USHORT", value: 1 },
    { name: "coverage", type: "TABLE", value: new table.Coverage(subtable.coverage) }
  ].concat(table.tableList("altSet", subtable.alternateSets, function(alternateSet) {
    return new table.Table("alternateSetTable", table.ushortList("alternate", alternateSet));
  })));
};
subtableMakers[4] = function makeLookup4(subtable) {
  check.assert(subtable.substFormat === 1, "Lookup type 4 substFormat must be 1.");
  return new table.Table("substitutionTable", [
    { name: "substFormat", type: "USHORT", value: 1 },
    { name: "coverage", type: "TABLE", value: new table.Coverage(subtable.coverage) }
  ].concat(table.tableList("ligSet", subtable.ligatureSets, function(ligatureSet) {
    return new table.Table("ligatureSetTable", table.tableList("ligature", ligatureSet, function(ligature) {
      return new table.Table("ligatureTable", [{ name: "ligGlyph", type: "USHORT", value: ligature.ligGlyph }].concat(table.ushortList("component", ligature.components, ligature.components.length + 1)));
    }));
  })));
};
subtableMakers[6] = function makeLookup6(subtable) {
  if (subtable.substFormat === 1) {
    var returnTable = new table.Table("chainContextTable", [
      { name: "substFormat", type: "USHORT", value: subtable.substFormat },
      { name: "coverage", type: "TABLE", value: new table.Coverage(subtable.coverage) }
    ].concat(table.tableList("chainRuleSet", subtable.chainRuleSets, function(chainRuleSet) {
      return new table.Table("chainRuleSetTable", table.tableList("chainRule", chainRuleSet, function(chainRule) {
        var tableData2 = table.ushortList("backtrackGlyph", chainRule.backtrack, chainRule.backtrack.length).concat(table.ushortList("inputGlyph", chainRule.input, chainRule.input.length + 1)).concat(table.ushortList("lookaheadGlyph", chainRule.lookahead, chainRule.lookahead.length)).concat(table.ushortList("substitution", [], chainRule.lookupRecords.length));
        chainRule.lookupRecords.forEach(function(record, i) {
          tableData2 = tableData2.concat({ name: "sequenceIndex" + i, type: "USHORT", value: record.sequenceIndex }).concat({ name: "lookupListIndex" + i, type: "USHORT", value: record.lookupListIndex });
        });
        return new table.Table("chainRuleTable", tableData2);
      }));
    })));
    return returnTable;
  } else if (subtable.substFormat === 2) {
    check.assert(false, "lookup type 6 format 2 is not yet supported.");
  } else if (subtable.substFormat === 3) {
    var tableData = [
      { name: "substFormat", type: "USHORT", value: subtable.substFormat }
    ];
    tableData.push({ name: "backtrackGlyphCount", type: "USHORT", value: subtable.backtrackCoverage.length });
    subtable.backtrackCoverage.forEach(function(coverage, i) {
      tableData.push({ name: "backtrackCoverage" + i, type: "TABLE", value: new table.Coverage(coverage) });
    });
    tableData.push({ name: "inputGlyphCount", type: "USHORT", value: subtable.inputCoverage.length });
    subtable.inputCoverage.forEach(function(coverage, i) {
      tableData.push({ name: "inputCoverage" + i, type: "TABLE", value: new table.Coverage(coverage) });
    });
    tableData.push({ name: "lookaheadGlyphCount", type: "USHORT", value: subtable.lookaheadCoverage.length });
    subtable.lookaheadCoverage.forEach(function(coverage, i) {
      tableData.push({ name: "lookaheadCoverage" + i, type: "TABLE", value: new table.Coverage(coverage) });
    });
    tableData.push({ name: "substitutionCount", type: "USHORT", value: subtable.lookupRecords.length });
    subtable.lookupRecords.forEach(function(record, i) {
      tableData = tableData.concat({ name: "sequenceIndex" + i, type: "USHORT", value: record.sequenceIndex }).concat({ name: "lookupListIndex" + i, type: "USHORT", value: record.lookupListIndex });
    });
    var returnTable$1 = new table.Table("chainContextTable", tableData);
    return returnTable$1;
  }
  check.assert(false, "lookup type 6 format must be 1, 2 or 3.");
};
function makeGsubTable(gsub2) {
  return new table.Table("GSUB", [
    { name: "version", type: "ULONG", value: 65536 },
    { name: "scripts", type: "TABLE", value: new table.ScriptList(gsub2.scripts) },
    { name: "features", type: "TABLE", value: new table.FeatureList(gsub2.features) },
    { name: "lookups", type: "TABLE", value: new table.LookupList(gsub2.lookups, subtableMakers) }
  ]);
}
var gsub = { parse: parseGsubTable, make: makeGsubTable };
function parseMetaTable(data, start) {
  var p = new parse.Parser(data, start);
  var tableVersion = p.parseULong();
  check.argument(tableVersion === 1, "Unsupported META table version.");
  p.parseULong();
  p.parseULong();
  var numDataMaps = p.parseULong();
  var tags = {};
  for (var i = 0;i < numDataMaps; i++) {
    var tag = p.parseTag();
    var dataOffset = p.parseULong();
    var dataLength = p.parseULong();
    var text = decode.UTF8(data, start + dataOffset, dataLength);
    tags[tag] = text;
  }
  return tags;
}
function makeMetaTable(tags) {
  var numTags = Object.keys(tags).length;
  var stringPool = "";
  var stringPoolOffset = 16 + numTags * 12;
  var result = new table.Table("meta", [
    { name: "version", type: "ULONG", value: 1 },
    { name: "flags", type: "ULONG", value: 0 },
    { name: "offset", type: "ULONG", value: stringPoolOffset },
    { name: "numTags", type: "ULONG", value: numTags }
  ]);
  for (var tag in tags) {
    var pos = stringPool.length;
    stringPool += tags[tag];
    result.fields.push({ name: "tag " + tag, type: "TAG", value: tag });
    result.fields.push({ name: "offset " + tag, type: "ULONG", value: stringPoolOffset + pos });
    result.fields.push({ name: "length " + tag, type: "ULONG", value: tags[tag].length });
  }
  result.fields.push({ name: "stringPool", type: "CHARARRAY", value: stringPool });
  return result;
}
var meta = { parse: parseMetaTable, make: makeMetaTable };
function log2(v) {
  return Math.log(v) / Math.log(2) | 0;
}
function computeCheckSum(bytes) {
  while (bytes.length % 4 !== 0) {
    bytes.push(0);
  }
  var sum = 0;
  for (var i = 0;i < bytes.length; i += 4) {
    sum += (bytes[i] << 24) + (bytes[i + 1] << 16) + (bytes[i + 2] << 8) + bytes[i + 3];
  }
  sum %= Math.pow(2, 32);
  return sum;
}
function makeTableRecord(tag, checkSum, offset2, length) {
  return new table.Record("Table Record", [
    { name: "tag", type: "TAG", value: tag !== undefined ? tag : "" },
    { name: "checkSum", type: "ULONG", value: checkSum !== undefined ? checkSum : 0 },
    { name: "offset", type: "ULONG", value: offset2 !== undefined ? offset2 : 0 },
    { name: "length", type: "ULONG", value: length !== undefined ? length : 0 }
  ]);
}
function makeSfntTable(tables) {
  var sfnt2 = new table.Table("sfnt", [
    { name: "version", type: "TAG", value: "OTTO" },
    { name: "numTables", type: "USHORT", value: 0 },
    { name: "searchRange", type: "USHORT", value: 0 },
    { name: "entrySelector", type: "USHORT", value: 0 },
    { name: "rangeShift", type: "USHORT", value: 0 }
  ]);
  sfnt2.tables = tables;
  sfnt2.numTables = tables.length;
  var highestPowerOf2 = Math.pow(2, log2(sfnt2.numTables));
  sfnt2.searchRange = 16 * highestPowerOf2;
  sfnt2.entrySelector = log2(highestPowerOf2);
  sfnt2.rangeShift = sfnt2.numTables * 16 - sfnt2.searchRange;
  var recordFields = [];
  var tableFields = [];
  var offset2 = sfnt2.sizeOf() + makeTableRecord().sizeOf() * sfnt2.numTables;
  while (offset2 % 4 !== 0) {
    offset2 += 1;
    tableFields.push({ name: "padding", type: "BYTE", value: 0 });
  }
  for (var i = 0;i < tables.length; i += 1) {
    var t = tables[i];
    check.argument(t.tableName.length === 4, "Table name" + t.tableName + " is invalid.");
    var tableLength = t.sizeOf();
    var tableRecord = makeTableRecord(t.tableName, computeCheckSum(t.encode()), offset2, tableLength);
    recordFields.push({ name: tableRecord.tag + " Table Record", type: "RECORD", value: tableRecord });
    tableFields.push({ name: t.tableName + " table", type: "RECORD", value: t });
    offset2 += tableLength;
    check.argument(!isNaN(offset2), "Something went wrong calculating the offset.");
    while (offset2 % 4 !== 0) {
      offset2 += 1;
      tableFields.push({ name: "padding", type: "BYTE", value: 0 });
    }
  }
  recordFields.sort(function(r1, r2) {
    if (r1.value.tag > r2.value.tag) {
      return 1;
    } else {
      return -1;
    }
  });
  sfnt2.fields = sfnt2.fields.concat(recordFields);
  sfnt2.fields = sfnt2.fields.concat(tableFields);
  return sfnt2;
}
function metricsForChar(font, chars, notFoundMetrics) {
  for (var i = 0;i < chars.length; i += 1) {
    var glyphIndex = font.charToGlyphIndex(chars[i]);
    if (glyphIndex > 0) {
      var glyph = font.glyphs.get(glyphIndex);
      return glyph.getMetrics();
    }
  }
  return notFoundMetrics;
}
function average(vs) {
  var sum = 0;
  for (var i = 0;i < vs.length; i += 1) {
    sum += vs[i];
  }
  return sum / vs.length;
}
function fontToSfntTable(font) {
  var xMins = [];
  var yMins = [];
  var xMaxs = [];
  var yMaxs = [];
  var advanceWidths = [];
  var leftSideBearings = [];
  var rightSideBearings = [];
  var firstCharIndex;
  var lastCharIndex = 0;
  var ulUnicodeRange1 = 0;
  var ulUnicodeRange2 = 0;
  var ulUnicodeRange3 = 0;
  var ulUnicodeRange4 = 0;
  for (var i = 0;i < font.glyphs.length; i += 1) {
    var glyph = font.glyphs.get(i);
    var unicode = glyph.unicode | 0;
    if (isNaN(glyph.advanceWidth)) {
      throw new Error("Glyph " + glyph.name + " (" + i + "): advanceWidth is not a number.");
    }
    if (firstCharIndex > unicode || firstCharIndex === undefined) {
      if (unicode > 0) {
        firstCharIndex = unicode;
      }
    }
    if (lastCharIndex < unicode) {
      lastCharIndex = unicode;
    }
    var position = os2.getUnicodeRange(unicode);
    if (position < 32) {
      ulUnicodeRange1 |= 1 << position;
    } else if (position < 64) {
      ulUnicodeRange2 |= 1 << position - 32;
    } else if (position < 96) {
      ulUnicodeRange3 |= 1 << position - 64;
    } else if (position < 123) {
      ulUnicodeRange4 |= 1 << position - 96;
    } else {
      throw new Error("Unicode ranges bits > 123 are reserved for internal usage");
    }
    if (glyph.name === ".notdef") {
      continue;
    }
    var metrics = glyph.getMetrics();
    xMins.push(metrics.xMin);
    yMins.push(metrics.yMin);
    xMaxs.push(metrics.xMax);
    yMaxs.push(metrics.yMax);
    leftSideBearings.push(metrics.leftSideBearing);
    rightSideBearings.push(metrics.rightSideBearing);
    advanceWidths.push(glyph.advanceWidth);
  }
  var globals = {
    xMin: Math.min.apply(null, xMins),
    yMin: Math.min.apply(null, yMins),
    xMax: Math.max.apply(null, xMaxs),
    yMax: Math.max.apply(null, yMaxs),
    advanceWidthMax: Math.max.apply(null, advanceWidths),
    advanceWidthAvg: average(advanceWidths),
    minLeftSideBearing: Math.min.apply(null, leftSideBearings),
    maxLeftSideBearing: Math.max.apply(null, leftSideBearings),
    minRightSideBearing: Math.min.apply(null, rightSideBearings)
  };
  globals.ascender = font.ascender;
  globals.descender = font.descender;
  var headTable = head.make({
    flags: 3,
    unitsPerEm: font.unitsPerEm,
    xMin: globals.xMin,
    yMin: globals.yMin,
    xMax: globals.xMax,
    yMax: globals.yMax,
    lowestRecPPEM: 3,
    createdTimestamp: font.createdTimestamp
  });
  var hheaTable = hhea.make({
    ascender: globals.ascender,
    descender: globals.descender,
    advanceWidthMax: globals.advanceWidthMax,
    minLeftSideBearing: globals.minLeftSideBearing,
    minRightSideBearing: globals.minRightSideBearing,
    xMaxExtent: globals.maxLeftSideBearing + (globals.xMax - globals.xMin),
    numberOfHMetrics: font.glyphs.length
  });
  var maxpTable = maxp.make(font.glyphs.length);
  var os2Table = os2.make(Object.assign({
    xAvgCharWidth: Math.round(globals.advanceWidthAvg),
    usFirstCharIndex: firstCharIndex,
    usLastCharIndex: lastCharIndex,
    ulUnicodeRange1,
    ulUnicodeRange2,
    ulUnicodeRange3,
    ulUnicodeRange4,
    sTypoAscender: globals.ascender,
    sTypoDescender: globals.descender,
    sTypoLineGap: 0,
    usWinAscent: globals.yMax,
    usWinDescent: Math.abs(globals.yMin),
    ulCodePageRange1: 1,
    sxHeight: metricsForChar(font, "xyvw", { yMax: Math.round(globals.ascender / 2) }).yMax,
    sCapHeight: metricsForChar(font, "HIKLEFJMNTZBDPRAGOQSUVWXY", globals).yMax,
    usDefaultChar: font.hasChar(" ") ? 32 : 0,
    usBreakChar: font.hasChar(" ") ? 32 : 0
  }, font.tables.os2));
  var hmtxTable = hmtx.make(font.glyphs);
  var cmapTable = cmap.make(font.glyphs);
  var englishFamilyName = font.getEnglishName("fontFamily");
  var englishStyleName = font.getEnglishName("fontSubfamily");
  var englishFullName = englishFamilyName + " " + englishStyleName;
  var postScriptName = font.getEnglishName("postScriptName");
  if (!postScriptName) {
    postScriptName = englishFamilyName.replace(/\s/g, "") + "-" + englishStyleName;
  }
  var names = {};
  for (var n in font.names) {
    names[n] = font.names[n];
  }
  if (!names.uniqueID) {
    names.uniqueID = { en: font.getEnglishName("manufacturer") + ":" + englishFullName };
  }
  if (!names.postScriptName) {
    names.postScriptName = { en: postScriptName };
  }
  if (!names.preferredFamily) {
    names.preferredFamily = font.names.fontFamily;
  }
  if (!names.preferredSubfamily) {
    names.preferredSubfamily = font.names.fontSubfamily;
  }
  var languageTags = [];
  var nameTable = _name.make(names, languageTags);
  var ltagTable = languageTags.length > 0 ? ltag.make(languageTags) : undefined;
  var postTable = post.make();
  var cffTable = cff.make(font.glyphs, {
    version: font.getEnglishName("version"),
    fullName: englishFullName,
    familyName: englishFamilyName,
    weightName: englishStyleName,
    postScriptName,
    unitsPerEm: font.unitsPerEm,
    fontBBox: [0, globals.yMin, globals.ascender, globals.advanceWidthMax]
  });
  var metaTable = font.metas && Object.keys(font.metas).length > 0 ? meta.make(font.metas) : undefined;
  var tables = [headTable, hheaTable, maxpTable, os2Table, nameTable, cmapTable, postTable, cffTable, hmtxTable];
  if (ltagTable) {
    tables.push(ltagTable);
  }
  if (font.tables.gsub) {
    tables.push(gsub.make(font.tables.gsub));
  }
  if (metaTable) {
    tables.push(metaTable);
  }
  var sfntTable = makeSfntTable(tables);
  var bytes = sfntTable.encode();
  var checkSum = computeCheckSum(bytes);
  var tableFields = sfntTable.fields;
  var checkSumAdjusted = false;
  for (var i$1 = 0;i$1 < tableFields.length; i$1 += 1) {
    if (tableFields[i$1].name === "head table") {
      tableFields[i$1].value.checkSumAdjustment = 2981146554 - checkSum;
      checkSumAdjusted = true;
      break;
    }
  }
  if (!checkSumAdjusted) {
    throw new Error("Could not find head table with checkSum to adjust.");
  }
  return sfntTable;
}
var sfnt = { make: makeSfntTable, fontToTable: fontToSfntTable, computeCheckSum };
function searchTag(arr, tag) {
  var imin = 0;
  var imax = arr.length - 1;
  while (imin <= imax) {
    var imid = imin + imax >>> 1;
    var val = arr[imid].tag;
    if (val === tag) {
      return imid;
    } else if (val < tag) {
      imin = imid + 1;
    } else {
      imax = imid - 1;
    }
  }
  return -imin - 1;
}
function binSearch(arr, value) {
  var imin = 0;
  var imax = arr.length - 1;
  while (imin <= imax) {
    var imid = imin + imax >>> 1;
    var val = arr[imid];
    if (val === value) {
      return imid;
    } else if (val < value) {
      imin = imid + 1;
    } else {
      imax = imid - 1;
    }
  }
  return -imin - 1;
}
function searchRange(ranges, value) {
  var range2;
  var imin = 0;
  var imax = ranges.length - 1;
  while (imin <= imax) {
    var imid = imin + imax >>> 1;
    range2 = ranges[imid];
    var start = range2.start;
    if (start === value) {
      return range2;
    } else if (start < value) {
      imin = imid + 1;
    } else {
      imax = imid - 1;
    }
  }
  if (imin > 0) {
    range2 = ranges[imin - 1];
    if (value > range2.end) {
      return 0;
    }
    return range2;
  }
}
function Layout(font, tableName) {
  this.font = font;
  this.tableName = tableName;
}
Layout.prototype = {
  searchTag,
  binSearch,
  getTable: function(create) {
    var layout = this.font.tables[this.tableName];
    if (!layout && create) {
      layout = this.font.tables[this.tableName] = this.createDefaultTable();
    }
    return layout;
  },
  getScriptNames: function() {
    var layout = this.getTable();
    if (!layout) {
      return [];
    }
    return layout.scripts.map(function(script) {
      return script.tag;
    });
  },
  getDefaultScriptName: function() {
    var layout = this.getTable();
    if (!layout) {
      return;
    }
    var hasLatn = false;
    for (var i = 0;i < layout.scripts.length; i++) {
      var name = layout.scripts[i].tag;
      if (name === "DFLT") {
        return name;
      }
      if (name === "latn") {
        hasLatn = true;
      }
    }
    if (hasLatn) {
      return "latn";
    }
  },
  getScriptTable: function(script, create) {
    var layout = this.getTable(create);
    if (layout) {
      script = script || "DFLT";
      var scripts = layout.scripts;
      var pos = searchTag(layout.scripts, script);
      if (pos >= 0) {
        return scripts[pos].script;
      } else if (create) {
        var scr = {
          tag: script,
          script: {
            defaultLangSys: { reserved: 0, reqFeatureIndex: 65535, featureIndexes: [] },
            langSysRecords: []
          }
        };
        scripts.splice(-1 - pos, 0, scr);
        return scr.script;
      }
    }
  },
  getLangSysTable: function(script, language, create) {
    var scriptTable = this.getScriptTable(script, create);
    if (scriptTable) {
      if (!language || language === "dflt" || language === "DFLT") {
        return scriptTable.defaultLangSys;
      }
      var pos = searchTag(scriptTable.langSysRecords, language);
      if (pos >= 0) {
        return scriptTable.langSysRecords[pos].langSys;
      } else if (create) {
        var langSysRecord = {
          tag: language,
          langSys: { reserved: 0, reqFeatureIndex: 65535, featureIndexes: [] }
        };
        scriptTable.langSysRecords.splice(-1 - pos, 0, langSysRecord);
        return langSysRecord.langSys;
      }
    }
  },
  getFeatureTable: function(script, language, feature, create) {
    var langSysTable2 = this.getLangSysTable(script, language, create);
    if (langSysTable2) {
      var featureRecord;
      var featIndexes = langSysTable2.featureIndexes;
      var allFeatures = this.font.tables[this.tableName].features;
      for (var i = 0;i < featIndexes.length; i++) {
        featureRecord = allFeatures[featIndexes[i]];
        if (featureRecord.tag === feature) {
          return featureRecord.feature;
        }
      }
      if (create) {
        var index = allFeatures.length;
        check.assert(index === 0 || feature >= allFeatures[index - 1].tag, "Features must be added in alphabetical order.");
        featureRecord = {
          tag: feature,
          feature: { params: 0, lookupListIndexes: [] }
        };
        allFeatures.push(featureRecord);
        featIndexes.push(index);
        return featureRecord.feature;
      }
    }
  },
  getLookupTables: function(script, language, feature, lookupType, create) {
    var featureTable = this.getFeatureTable(script, language, feature, create);
    var tables = [];
    if (featureTable) {
      var lookupTable;
      var lookupListIndexes = featureTable.lookupListIndexes;
      var allLookups = this.font.tables[this.tableName].lookups;
      for (var i = 0;i < lookupListIndexes.length; i++) {
        lookupTable = allLookups[lookupListIndexes[i]];
        if (lookupTable.lookupType === lookupType) {
          tables.push(lookupTable);
        }
      }
      if (tables.length === 0 && create) {
        lookupTable = {
          lookupType,
          lookupFlag: 0,
          subtables: [],
          markFilteringSet: undefined
        };
        var index = allLookups.length;
        allLookups.push(lookupTable);
        lookupListIndexes.push(index);
        return [lookupTable];
      }
    }
    return tables;
  },
  getGlyphClass: function(classDefTable, glyphIndex) {
    switch (classDefTable.format) {
      case 1:
        if (classDefTable.startGlyph <= glyphIndex && glyphIndex < classDefTable.startGlyph + classDefTable.classes.length) {
          return classDefTable.classes[glyphIndex - classDefTable.startGlyph];
        }
        return 0;
      case 2:
        var range2 = searchRange(classDefTable.ranges, glyphIndex);
        return range2 ? range2.classId : 0;
    }
  },
  getCoverageIndex: function(coverageTable, glyphIndex) {
    switch (coverageTable.format) {
      case 1:
        var index = binSearch(coverageTable.glyphs, glyphIndex);
        return index >= 0 ? index : -1;
      case 2:
        var range2 = searchRange(coverageTable.ranges, glyphIndex);
        return range2 ? range2.index + glyphIndex - range2.start : -1;
    }
  },
  expandCoverage: function(coverageTable) {
    if (coverageTable.format === 1) {
      return coverageTable.glyphs;
    } else {
      var glyphs = [];
      var ranges = coverageTable.ranges;
      for (var i = 0;i < ranges.length; i++) {
        var range2 = ranges[i];
        var start = range2.start;
        var end = range2.end;
        for (var j = start;j <= end; j++) {
          glyphs.push(j);
        }
      }
      return glyphs;
    }
  }
};
function Position(font) {
  Layout.call(this, font, "gpos");
}
Position.prototype = Layout.prototype;
Position.prototype.init = function() {
  var script = this.getDefaultScriptName();
  this.defaultKerningTables = this.getKerningTables(script);
};
Position.prototype.getKerningValue = function(kerningLookups, leftIndex, rightIndex) {
  for (var i = 0;i < kerningLookups.length; i++) {
    var subtables = kerningLookups[i].subtables;
    for (var j = 0;j < subtables.length; j++) {
      var subtable = subtables[j];
      var covIndex = this.getCoverageIndex(subtable.coverage, leftIndex);
      if (covIndex < 0) {
        continue;
      }
      switch (subtable.posFormat) {
        case 1:
          var pairSet = subtable.pairSets[covIndex];
          for (var k = 0;k < pairSet.length; k++) {
            var pair = pairSet[k];
            if (pair.secondGlyph === rightIndex) {
              return pair.value1 && pair.value1.xAdvance || 0;
            }
          }
          break;
        case 2:
          var class1 = this.getGlyphClass(subtable.classDef1, leftIndex);
          var class2 = this.getGlyphClass(subtable.classDef2, rightIndex);
          var pair$1 = subtable.classRecords[class1][class2];
          return pair$1.value1 && pair$1.value1.xAdvance || 0;
      }
    }
  }
  return 0;
};
Position.prototype.getKerningTables = function(script, language) {
  if (this.font.tables.gpos) {
    return this.getLookupTables(script, language, "kern", 2);
  }
};
function Substitution(font) {
  Layout.call(this, font, "gsub");
}
function arraysEqual(ar1, ar2) {
  var n = ar1.length;
  if (n !== ar2.length) {
    return false;
  }
  for (var i = 0;i < n; i++) {
    if (ar1[i] !== ar2[i]) {
      return false;
    }
  }
  return true;
}
function getSubstFormat(lookupTable, format, defaultSubtable) {
  var subtables = lookupTable.subtables;
  for (var i = 0;i < subtables.length; i++) {
    var subtable = subtables[i];
    if (subtable.substFormat === format) {
      return subtable;
    }
  }
  if (defaultSubtable) {
    subtables.push(defaultSubtable);
    return defaultSubtable;
  }
  return;
}
Substitution.prototype = Layout.prototype;
Substitution.prototype.createDefaultTable = function() {
  return {
    version: 1,
    scripts: [{
      tag: "DFLT",
      script: {
        defaultLangSys: { reserved: 0, reqFeatureIndex: 65535, featureIndexes: [] },
        langSysRecords: []
      }
    }],
    features: [],
    lookups: []
  };
};
Substitution.prototype.getSingle = function(feature, script, language) {
  var substitutions = [];
  var lookupTables = this.getLookupTables(script, language, feature, 1);
  for (var idx = 0;idx < lookupTables.length; idx++) {
    var subtables = lookupTables[idx].subtables;
    for (var i = 0;i < subtables.length; i++) {
      var subtable = subtables[i];
      var glyphs = this.expandCoverage(subtable.coverage);
      var j = undefined;
      if (subtable.substFormat === 1) {
        var delta = subtable.deltaGlyphId;
        for (j = 0;j < glyphs.length; j++) {
          var glyph = glyphs[j];
          substitutions.push({ sub: glyph, by: glyph + delta });
        }
      } else {
        var substitute = subtable.substitute;
        for (j = 0;j < glyphs.length; j++) {
          substitutions.push({ sub: glyphs[j], by: substitute[j] });
        }
      }
    }
  }
  return substitutions;
};
Substitution.prototype.getMultiple = function(feature, script, language) {
  var substitutions = [];
  var lookupTables = this.getLookupTables(script, language, feature, 2);
  for (var idx = 0;idx < lookupTables.length; idx++) {
    var subtables = lookupTables[idx].subtables;
    for (var i = 0;i < subtables.length; i++) {
      var subtable = subtables[i];
      var glyphs = this.expandCoverage(subtable.coverage);
      var j = undefined;
      for (j = 0;j < glyphs.length; j++) {
        var glyph = glyphs[j];
        var replacements = subtable.sequences[j];
        substitutions.push({ sub: glyph, by: replacements });
      }
    }
  }
  return substitutions;
};
Substitution.prototype.getAlternates = function(feature, script, language) {
  var alternates = [];
  var lookupTables = this.getLookupTables(script, language, feature, 3);
  for (var idx = 0;idx < lookupTables.length; idx++) {
    var subtables = lookupTables[idx].subtables;
    for (var i = 0;i < subtables.length; i++) {
      var subtable = subtables[i];
      var glyphs = this.expandCoverage(subtable.coverage);
      var alternateSets = subtable.alternateSets;
      for (var j = 0;j < glyphs.length; j++) {
        alternates.push({ sub: glyphs[j], by: alternateSets[j] });
      }
    }
  }
  return alternates;
};
Substitution.prototype.getLigatures = function(feature, script, language) {
  var ligatures = [];
  var lookupTables = this.getLookupTables(script, language, feature, 4);
  for (var idx = 0;idx < lookupTables.length; idx++) {
    var subtables = lookupTables[idx].subtables;
    for (var i = 0;i < subtables.length; i++) {
      var subtable = subtables[i];
      var glyphs = this.expandCoverage(subtable.coverage);
      var ligatureSets = subtable.ligatureSets;
      for (var j = 0;j < glyphs.length; j++) {
        var startGlyph = glyphs[j];
        var ligSet = ligatureSets[j];
        for (var k = 0;k < ligSet.length; k++) {
          var lig = ligSet[k];
          ligatures.push({
            sub: [startGlyph].concat(lig.components),
            by: lig.ligGlyph
          });
        }
      }
    }
  }
  return ligatures;
};
Substitution.prototype.addSingle = function(feature, substitution, script, language) {
  var lookupTable = this.getLookupTables(script, language, feature, 1, true)[0];
  var subtable = getSubstFormat(lookupTable, 2, {
    substFormat: 2,
    coverage: { format: 1, glyphs: [] },
    substitute: []
  });
  check.assert(subtable.coverage.format === 1, "Single: unable to modify coverage table format " + subtable.coverage.format);
  var coverageGlyph = substitution.sub;
  var pos = this.binSearch(subtable.coverage.glyphs, coverageGlyph);
  if (pos < 0) {
    pos = -1 - pos;
    subtable.coverage.glyphs.splice(pos, 0, coverageGlyph);
    subtable.substitute.splice(pos, 0, 0);
  }
  subtable.substitute[pos] = substitution.by;
};
Substitution.prototype.addMultiple = function(feature, substitution, script, language) {
  check.assert(substitution.by instanceof Array && substitution.by.length > 1, 'Multiple: "by" must be an array of two or more ids');
  var lookupTable = this.getLookupTables(script, language, feature, 2, true)[0];
  var subtable = getSubstFormat(lookupTable, 1, {
    substFormat: 1,
    coverage: { format: 1, glyphs: [] },
    sequences: []
  });
  check.assert(subtable.coverage.format === 1, "Multiple: unable to modify coverage table format " + subtable.coverage.format);
  var coverageGlyph = substitution.sub;
  var pos = this.binSearch(subtable.coverage.glyphs, coverageGlyph);
  if (pos < 0) {
    pos = -1 - pos;
    subtable.coverage.glyphs.splice(pos, 0, coverageGlyph);
    subtable.sequences.splice(pos, 0, 0);
  }
  subtable.sequences[pos] = substitution.by;
};
Substitution.prototype.addAlternate = function(feature, substitution, script, language) {
  var lookupTable = this.getLookupTables(script, language, feature, 3, true)[0];
  var subtable = getSubstFormat(lookupTable, 1, {
    substFormat: 1,
    coverage: { format: 1, glyphs: [] },
    alternateSets: []
  });
  check.assert(subtable.coverage.format === 1, "Alternate: unable to modify coverage table format " + subtable.coverage.format);
  var coverageGlyph = substitution.sub;
  var pos = this.binSearch(subtable.coverage.glyphs, coverageGlyph);
  if (pos < 0) {
    pos = -1 - pos;
    subtable.coverage.glyphs.splice(pos, 0, coverageGlyph);
    subtable.alternateSets.splice(pos, 0, 0);
  }
  subtable.alternateSets[pos] = substitution.by;
};
Substitution.prototype.addLigature = function(feature, ligature, script, language) {
  var lookupTable = this.getLookupTables(script, language, feature, 4, true)[0];
  var subtable = lookupTable.subtables[0];
  if (!subtable) {
    subtable = {
      substFormat: 1,
      coverage: { format: 1, glyphs: [] },
      ligatureSets: []
    };
    lookupTable.subtables[0] = subtable;
  }
  check.assert(subtable.coverage.format === 1, "Ligature: unable to modify coverage table format " + subtable.coverage.format);
  var coverageGlyph = ligature.sub[0];
  var ligComponents = ligature.sub.slice(1);
  var ligatureTable = {
    ligGlyph: ligature.by,
    components: ligComponents
  };
  var pos = this.binSearch(subtable.coverage.glyphs, coverageGlyph);
  if (pos >= 0) {
    var ligatureSet = subtable.ligatureSets[pos];
    for (var i = 0;i < ligatureSet.length; i++) {
      if (arraysEqual(ligatureSet[i].components, ligComponents)) {
        return;
      }
    }
    ligatureSet.push(ligatureTable);
  } else {
    pos = -1 - pos;
    subtable.coverage.glyphs.splice(pos, 0, coverageGlyph);
    subtable.ligatureSets.splice(pos, 0, [ligatureTable]);
  }
};
Substitution.prototype.getFeature = function(feature, script, language) {
  if (/ss\d\d/.test(feature)) {
    return this.getSingle(feature, script, language);
  }
  switch (feature) {
    case "aalt":
    case "salt":
      return this.getSingle(feature, script, language).concat(this.getAlternates(feature, script, language));
    case "dlig":
    case "liga":
    case "rlig":
      return this.getLigatures(feature, script, language);
    case "ccmp":
      return this.getMultiple(feature, script, language).concat(this.getLigatures(feature, script, language));
    case "stch":
      return this.getMultiple(feature, script, language);
  }
  return;
};
Substitution.prototype.add = function(feature, sub, script, language) {
  if (/ss\d\d/.test(feature)) {
    return this.addSingle(feature, sub, script, language);
  }
  switch (feature) {
    case "aalt":
    case "salt":
      if (typeof sub.by === "number") {
        return this.addSingle(feature, sub, script, language);
      }
      return this.addAlternate(feature, sub, script, language);
    case "dlig":
    case "liga":
    case "rlig":
      return this.addLigature(feature, sub, script, language);
    case "ccmp":
      if (sub.by instanceof Array) {
        return this.addMultiple(feature, sub, script, language);
      }
      return this.addLigature(feature, sub, script, language);
  }
  return;
};
function isBrowser() {
  return typeof window !== "undefined";
}
function arrayBufferToNodeBuffer(ab) {
  var buffer = new Buffer(ab.byteLength);
  var view = new Uint8Array(ab);
  for (var i = 0;i < buffer.length; ++i) {
    buffer[i] = view[i];
  }
  return buffer;
}
function checkArgument(expression, message) {
  if (!expression) {
    throw message;
  }
}
function parseGlyphCoordinate(p, flag, previousValue, shortVectorBitMask, sameBitMask) {
  var v;
  if ((flag & shortVectorBitMask) > 0) {
    v = p.parseByte();
    if ((flag & sameBitMask) === 0) {
      v = -v;
    }
    v = previousValue + v;
  } else {
    if ((flag & sameBitMask) > 0) {
      v = previousValue;
    } else {
      v = previousValue + p.parseShort();
    }
  }
  return v;
}
function parseGlyph(glyph, data, start) {
  var p = new parse.Parser(data, start);
  glyph.numberOfContours = p.parseShort();
  glyph._xMin = p.parseShort();
  glyph._yMin = p.parseShort();
  glyph._xMax = p.parseShort();
  glyph._yMax = p.parseShort();
  var flags;
  var flag;
  if (glyph.numberOfContours > 0) {
    var endPointIndices = glyph.endPointIndices = [];
    for (var i = 0;i < glyph.numberOfContours; i += 1) {
      endPointIndices.push(p.parseUShort());
    }
    glyph.instructionLength = p.parseUShort();
    glyph.instructions = [];
    for (var i$1 = 0;i$1 < glyph.instructionLength; i$1 += 1) {
      glyph.instructions.push(p.parseByte());
    }
    var numberOfCoordinates = endPointIndices[endPointIndices.length - 1] + 1;
    flags = [];
    for (var i$2 = 0;i$2 < numberOfCoordinates; i$2 += 1) {
      flag = p.parseByte();
      flags.push(flag);
      if ((flag & 8) > 0) {
        var repeatCount = p.parseByte();
        for (var j = 0;j < repeatCount; j += 1) {
          flags.push(flag);
          i$2 += 1;
        }
      }
    }
    check.argument(flags.length === numberOfCoordinates, "Bad flags.");
    if (endPointIndices.length > 0) {
      var points = [];
      var point;
      if (numberOfCoordinates > 0) {
        for (var i$3 = 0;i$3 < numberOfCoordinates; i$3 += 1) {
          flag = flags[i$3];
          point = {};
          point.onCurve = !!(flag & 1);
          point.lastPointOfContour = endPointIndices.indexOf(i$3) >= 0;
          points.push(point);
        }
        var px = 0;
        for (var i$4 = 0;i$4 < numberOfCoordinates; i$4 += 1) {
          flag = flags[i$4];
          point = points[i$4];
          point.x = parseGlyphCoordinate(p, flag, px, 2, 16);
          px = point.x;
        }
        var py = 0;
        for (var i$5 = 0;i$5 < numberOfCoordinates; i$5 += 1) {
          flag = flags[i$5];
          point = points[i$5];
          point.y = parseGlyphCoordinate(p, flag, py, 4, 32);
          py = point.y;
        }
      }
      glyph.points = points;
    } else {
      glyph.points = [];
    }
  } else if (glyph.numberOfContours === 0) {
    glyph.points = [];
  } else {
    glyph.isComposite = true;
    glyph.points = [];
    glyph.components = [];
    var moreComponents = true;
    while (moreComponents) {
      flags = p.parseUShort();
      var component = {
        glyphIndex: p.parseUShort(),
        xScale: 1,
        scale01: 0,
        scale10: 0,
        yScale: 1,
        dx: 0,
        dy: 0
      };
      if ((flags & 1) > 0) {
        if ((flags & 2) > 0) {
          component.dx = p.parseShort();
          component.dy = p.parseShort();
        } else {
          component.matchedPoints = [p.parseUShort(), p.parseUShort()];
        }
      } else {
        if ((flags & 2) > 0) {
          component.dx = p.parseChar();
          component.dy = p.parseChar();
        } else {
          component.matchedPoints = [p.parseByte(), p.parseByte()];
        }
      }
      if ((flags & 8) > 0) {
        component.xScale = component.yScale = p.parseF2Dot14();
      } else if ((flags & 64) > 0) {
        component.xScale = p.parseF2Dot14();
        component.yScale = p.parseF2Dot14();
      } else if ((flags & 128) > 0) {
        component.xScale = p.parseF2Dot14();
        component.scale01 = p.parseF2Dot14();
        component.scale10 = p.parseF2Dot14();
        component.yScale = p.parseF2Dot14();
      }
      glyph.components.push(component);
      moreComponents = !!(flags & 32);
    }
    if (flags & 256) {
      glyph.instructionLength = p.parseUShort();
      glyph.instructions = [];
      for (var i$6 = 0;i$6 < glyph.instructionLength; i$6 += 1) {
        glyph.instructions.push(p.parseByte());
      }
    }
  }
}
function transformPoints(points, transform) {
  var newPoints = [];
  for (var i = 0;i < points.length; i += 1) {
    var pt = points[i];
    var newPt = {
      x: transform.xScale * pt.x + transform.scale01 * pt.y + transform.dx,
      y: transform.scale10 * pt.x + transform.yScale * pt.y + transform.dy,
      onCurve: pt.onCurve,
      lastPointOfContour: pt.lastPointOfContour
    };
    newPoints.push(newPt);
  }
  return newPoints;
}
function getContours(points) {
  var contours = [];
  var currentContour = [];
  for (var i = 0;i < points.length; i += 1) {
    var pt = points[i];
    currentContour.push(pt);
    if (pt.lastPointOfContour) {
      contours.push(currentContour);
      currentContour = [];
    }
  }
  check.argument(currentContour.length === 0, "There are still points left in the current contour.");
  return contours;
}
function getPath(points) {
  var p = new Path;
  if (!points) {
    return p;
  }
  var contours = getContours(points);
  for (var contourIndex = 0;contourIndex < contours.length; ++contourIndex) {
    var contour = contours[contourIndex];
    var prev = null;
    var curr = contour[contour.length - 1];
    var next = contour[0];
    if (curr.onCurve) {
      p.moveTo(curr.x, curr.y);
    } else {
      if (next.onCurve) {
        p.moveTo(next.x, next.y);
      } else {
        var start = { x: (curr.x + next.x) * 0.5, y: (curr.y + next.y) * 0.5 };
        p.moveTo(start.x, start.y);
      }
    }
    for (var i = 0;i < contour.length; ++i) {
      prev = curr;
      curr = next;
      next = contour[(i + 1) % contour.length];
      if (curr.onCurve) {
        p.lineTo(curr.x, curr.y);
      } else {
        var next2 = next;
        if (!prev.onCurve) {
          (curr.x + prev.x) * 0.5, (curr.y + prev.y) * 0.5;
        }
        if (!next.onCurve) {
          next2 = { x: (curr.x + next.x) * 0.5, y: (curr.y + next.y) * 0.5 };
        }
        p.quadraticCurveTo(curr.x, curr.y, next2.x, next2.y);
      }
    }
    p.closePath();
  }
  return p;
}
function buildPath(glyphs, glyph) {
  if (glyph.isComposite) {
    for (var j = 0;j < glyph.components.length; j += 1) {
      var component = glyph.components[j];
      var componentGlyph = glyphs.get(component.glyphIndex);
      componentGlyph.getPath();
      if (componentGlyph.points) {
        var transformedPoints = undefined;
        if (component.matchedPoints === undefined) {
          transformedPoints = transformPoints(componentGlyph.points, component);
        } else {
          if (component.matchedPoints[0] > glyph.points.length - 1 || component.matchedPoints[1] > componentGlyph.points.length - 1) {
            throw Error("Matched points out of range in " + glyph.name);
          }
          var firstPt = glyph.points[component.matchedPoints[0]];
          var secondPt = componentGlyph.points[component.matchedPoints[1]];
          var transform = {
            xScale: component.xScale,
            scale01: component.scale01,
            scale10: component.scale10,
            yScale: component.yScale,
            dx: 0,
            dy: 0
          };
          secondPt = transformPoints([secondPt], transform)[0];
          transform.dx = firstPt.x - secondPt.x;
          transform.dy = firstPt.y - secondPt.y;
          transformedPoints = transformPoints(componentGlyph.points, transform);
        }
        glyph.points = glyph.points.concat(transformedPoints);
      }
    }
  }
  return getPath(glyph.points);
}
function parseGlyfTableAll(data, start, loca2, font) {
  var glyphs = new glyphset.GlyphSet(font);
  for (var i = 0;i < loca2.length - 1; i += 1) {
    var offset2 = loca2[i];
    var nextOffset = loca2[i + 1];
    if (offset2 !== nextOffset) {
      glyphs.push(i, glyphset.ttfGlyphLoader(font, i, parseGlyph, data, start + offset2, buildPath));
    } else {
      glyphs.push(i, glyphset.glyphLoader(font, i));
    }
  }
  return glyphs;
}
function parseGlyfTableOnLowMemory(data, start, loca2, font) {
  var glyphs = new glyphset.GlyphSet(font);
  font._push = function(i) {
    var offset2 = loca2[i];
    var nextOffset = loca2[i + 1];
    if (offset2 !== nextOffset) {
      glyphs.push(i, glyphset.ttfGlyphLoader(font, i, parseGlyph, data, start + offset2, buildPath));
    } else {
      glyphs.push(i, glyphset.glyphLoader(font, i));
    }
  };
  return glyphs;
}
function parseGlyfTable(data, start, loca2, font, opt) {
  if (opt.lowMemory) {
    return parseGlyfTableOnLowMemory(data, start, loca2, font);
  } else {
    return parseGlyfTableAll(data, start, loca2, font);
  }
}
var glyf = { getPath, parse: parseGlyfTable };
var instructionTable;
var exec;
var execGlyph;
var execComponent;
function Hinting(font) {
  this.font = font;
  this.getCommands = function(hPoints) {
    return glyf.getPath(hPoints).commands;
  };
  this._fpgmState = this._prepState = undefined;
  this._errorState = 0;
}
function roundOff(v) {
  return v;
}
function roundToGrid(v) {
  return Math.sign(v) * Math.round(Math.abs(v));
}
function roundToDoubleGrid(v) {
  return Math.sign(v) * Math.round(Math.abs(v * 2)) / 2;
}
function roundToHalfGrid(v) {
  return Math.sign(v) * (Math.round(Math.abs(v) + 0.5) - 0.5);
}
function roundUpToGrid(v) {
  return Math.sign(v) * Math.ceil(Math.abs(v));
}
function roundDownToGrid(v) {
  return Math.sign(v) * Math.floor(Math.abs(v));
}
var roundSuper = function(v) {
  var period = this.srPeriod;
  var phase = this.srPhase;
  var threshold = this.srThreshold;
  var sign = 1;
  if (v < 0) {
    v = -v;
    sign = -1;
  }
  v += threshold - phase;
  v = Math.trunc(v / period) * period;
  v += phase;
  if (v < 0) {
    return phase * sign;
  }
  return v * sign;
};
var xUnitVector = {
  x: 1,
  y: 0,
  axis: "x",
  distance: function(p1, p2, o1, o2) {
    return (o1 ? p1.xo : p1.x) - (o2 ? p2.xo : p2.x);
  },
  interpolate: function(p, rp1, rp2, pv) {
    var do1;
    var do2;
    var doa1;
    var doa2;
    var dm1;
    var dm2;
    var dt;
    if (!pv || pv === this) {
      do1 = p.xo - rp1.xo;
      do2 = p.xo - rp2.xo;
      dm1 = rp1.x - rp1.xo;
      dm2 = rp2.x - rp2.xo;
      doa1 = Math.abs(do1);
      doa2 = Math.abs(do2);
      dt = doa1 + doa2;
      if (dt === 0) {
        p.x = p.xo + (dm1 + dm2) / 2;
        return;
      }
      p.x = p.xo + (dm1 * doa2 + dm2 * doa1) / dt;
      return;
    }
    do1 = pv.distance(p, rp1, true, true);
    do2 = pv.distance(p, rp2, true, true);
    dm1 = pv.distance(rp1, rp1, false, true);
    dm2 = pv.distance(rp2, rp2, false, true);
    doa1 = Math.abs(do1);
    doa2 = Math.abs(do2);
    dt = doa1 + doa2;
    if (dt === 0) {
      xUnitVector.setRelative(p, p, (dm1 + dm2) / 2, pv, true);
      return;
    }
    xUnitVector.setRelative(p, p, (dm1 * doa2 + dm2 * doa1) / dt, pv, true);
  },
  normalSlope: Number.NEGATIVE_INFINITY,
  setRelative: function(p, rp, d, pv, org) {
    if (!pv || pv === this) {
      p.x = (org ? rp.xo : rp.x) + d;
      return;
    }
    var rpx = org ? rp.xo : rp.x;
    var rpy = org ? rp.yo : rp.y;
    var rpdx = rpx + d * pv.x;
    var rpdy = rpy + d * pv.y;
    p.x = rpdx + (p.y - rpdy) / pv.normalSlope;
  },
  slope: 0,
  touch: function(p) {
    p.xTouched = true;
  },
  touched: function(p) {
    return p.xTouched;
  },
  untouch: function(p) {
    p.xTouched = false;
  }
};
var yUnitVector = {
  x: 0,
  y: 1,
  axis: "y",
  distance: function(p1, p2, o1, o2) {
    return (o1 ? p1.yo : p1.y) - (o2 ? p2.yo : p2.y);
  },
  interpolate: function(p, rp1, rp2, pv) {
    var do1;
    var do2;
    var doa1;
    var doa2;
    var dm1;
    var dm2;
    var dt;
    if (!pv || pv === this) {
      do1 = p.yo - rp1.yo;
      do2 = p.yo - rp2.yo;
      dm1 = rp1.y - rp1.yo;
      dm2 = rp2.y - rp2.yo;
      doa1 = Math.abs(do1);
      doa2 = Math.abs(do2);
      dt = doa1 + doa2;
      if (dt === 0) {
        p.y = p.yo + (dm1 + dm2) / 2;
        return;
      }
      p.y = p.yo + (dm1 * doa2 + dm2 * doa1) / dt;
      return;
    }
    do1 = pv.distance(p, rp1, true, true);
    do2 = pv.distance(p, rp2, true, true);
    dm1 = pv.distance(rp1, rp1, false, true);
    dm2 = pv.distance(rp2, rp2, false, true);
    doa1 = Math.abs(do1);
    doa2 = Math.abs(do2);
    dt = doa1 + doa2;
    if (dt === 0) {
      yUnitVector.setRelative(p, p, (dm1 + dm2) / 2, pv, true);
      return;
    }
    yUnitVector.setRelative(p, p, (dm1 * doa2 + dm2 * doa1) / dt, pv, true);
  },
  normalSlope: 0,
  setRelative: function(p, rp, d, pv, org) {
    if (!pv || pv === this) {
      p.y = (org ? rp.yo : rp.y) + d;
      return;
    }
    var rpx = org ? rp.xo : rp.x;
    var rpy = org ? rp.yo : rp.y;
    var rpdx = rpx + d * pv.x;
    var rpdy = rpy + d * pv.y;
    p.y = rpdy + pv.normalSlope * (p.x - rpdx);
  },
  slope: Number.POSITIVE_INFINITY,
  touch: function(p) {
    p.yTouched = true;
  },
  touched: function(p) {
    return p.yTouched;
  },
  untouch: function(p) {
    p.yTouched = false;
  }
};
Object.freeze(xUnitVector);
Object.freeze(yUnitVector);
function UnitVector(x, y) {
  this.x = x;
  this.y = y;
  this.axis = undefined;
  this.slope = y / x;
  this.normalSlope = -x / y;
  Object.freeze(this);
}
UnitVector.prototype.distance = function(p1, p2, o1, o2) {
  return this.x * xUnitVector.distance(p1, p2, o1, o2) + this.y * yUnitVector.distance(p1, p2, o1, o2);
};
UnitVector.prototype.interpolate = function(p, rp1, rp2, pv) {
  var dm1;
  var dm2;
  var do1;
  var do2;
  var doa1;
  var doa2;
  var dt;
  do1 = pv.distance(p, rp1, true, true);
  do2 = pv.distance(p, rp2, true, true);
  dm1 = pv.distance(rp1, rp1, false, true);
  dm2 = pv.distance(rp2, rp2, false, true);
  doa1 = Math.abs(do1);
  doa2 = Math.abs(do2);
  dt = doa1 + doa2;
  if (dt === 0) {
    this.setRelative(p, p, (dm1 + dm2) / 2, pv, true);
    return;
  }
  this.setRelative(p, p, (dm1 * doa2 + dm2 * doa1) / dt, pv, true);
};
UnitVector.prototype.setRelative = function(p, rp, d, pv, org) {
  pv = pv || this;
  var rpx = org ? rp.xo : rp.x;
  var rpy = org ? rp.yo : rp.y;
  var rpdx = rpx + d * pv.x;
  var rpdy = rpy + d * pv.y;
  var pvns = pv.normalSlope;
  var fvs = this.slope;
  var px = p.x;
  var py = p.y;
  p.x = (fvs * px - pvns * rpdx + rpdy - py) / (fvs - pvns);
  p.y = fvs * (p.x - px) + py;
};
UnitVector.prototype.touch = function(p) {
  p.xTouched = true;
  p.yTouched = true;
};
function getUnitVector(x, y) {
  var d = Math.sqrt(x * x + y * y);
  x /= d;
  y /= d;
  if (x === 1 && y === 0) {
    return xUnitVector;
  } else if (x === 0 && y === 1) {
    return yUnitVector;
  } else {
    return new UnitVector(x, y);
  }
}
function HPoint(x, y, lastPointOfContour, onCurve) {
  this.x = this.xo = Math.round(x * 64) / 64;
  this.y = this.yo = Math.round(y * 64) / 64;
  this.lastPointOfContour = lastPointOfContour;
  this.onCurve = onCurve;
  this.prevPointOnContour = undefined;
  this.nextPointOnContour = undefined;
  this.xTouched = false;
  this.yTouched = false;
  Object.preventExtensions(this);
}
HPoint.prototype.nextTouched = function(v) {
  var p = this.nextPointOnContour;
  while (!v.touched(p) && p !== this) {
    p = p.nextPointOnContour;
  }
  return p;
};
HPoint.prototype.prevTouched = function(v) {
  var p = this.prevPointOnContour;
  while (!v.touched(p) && p !== this) {
    p = p.prevPointOnContour;
  }
  return p;
};
var HPZero = Object.freeze(new HPoint(0, 0));
var defaultState = {
  cvCutIn: 17 / 16,
  deltaBase: 9,
  deltaShift: 0.125,
  loop: 1,
  minDis: 1,
  autoFlip: true
};
function State(env, prog) {
  this.env = env;
  this.stack = [];
  this.prog = prog;
  switch (env) {
    case "glyf":
      this.zp0 = this.zp1 = this.zp2 = 1;
      this.rp0 = this.rp1 = this.rp2 = 0;
    case "prep":
      this.fv = this.pv = this.dpv = xUnitVector;
      this.round = roundToGrid;
  }
}
Hinting.prototype.exec = function(glyph, ppem) {
  if (typeof ppem !== "number") {
    throw new Error("Point size is not a number!");
  }
  if (this._errorState > 2) {
    return;
  }
  var font = this.font;
  var prepState = this._prepState;
  if (!prepState || prepState.ppem !== ppem) {
    var fpgmState = this._fpgmState;
    if (!fpgmState) {
      State.prototype = defaultState;
      fpgmState = this._fpgmState = new State("fpgm", font.tables.fpgm);
      fpgmState.funcs = [];
      fpgmState.font = font;
      if (exports_replicad.DEBUG) {
        console.log("---EXEC FPGM---");
        fpgmState.step = -1;
      }
      try {
        exec(fpgmState);
      } catch (e) {
        console.log("Hinting error in FPGM:" + e);
        this._errorState = 3;
        return;
      }
    }
    State.prototype = fpgmState;
    prepState = this._prepState = new State("prep", font.tables.prep);
    prepState.ppem = ppem;
    var oCvt = font.tables.cvt;
    if (oCvt) {
      var cvt = prepState.cvt = new Array(oCvt.length);
      var scale2 = ppem / font.unitsPerEm;
      for (var c = 0;c < oCvt.length; c++) {
        cvt[c] = oCvt[c] * scale2;
      }
    } else {
      prepState.cvt = [];
    }
    if (exports_replicad.DEBUG) {
      console.log("---EXEC PREP---");
      prepState.step = -1;
    }
    try {
      exec(prepState);
    } catch (e) {
      if (this._errorState < 2) {
        console.log("Hinting error in PREP:" + e);
      }
      this._errorState = 2;
    }
  }
  if (this._errorState > 1) {
    return;
  }
  try {
    return execGlyph(glyph, prepState);
  } catch (e) {
    if (this._errorState < 1) {
      console.log("Hinting error:" + e);
      console.log("Note: further hinting errors are silenced");
    }
    this._errorState = 1;
    return;
  }
};
execGlyph = function(glyph, prepState) {
  var xScale = prepState.ppem / prepState.font.unitsPerEm;
  var yScale = xScale;
  var components = glyph.components;
  var contours;
  var gZone;
  var state;
  State.prototype = prepState;
  if (!components) {
    state = new State("glyf", glyph.instructions);
    if (exports_replicad.DEBUG) {
      console.log("---EXEC GLYPH---");
      state.step = -1;
    }
    execComponent(glyph, state, xScale, yScale);
    gZone = state.gZone;
  } else {
    var font = prepState.font;
    gZone = [];
    contours = [];
    for (var i = 0;i < components.length; i++) {
      var c = components[i];
      var cg = font.glyphs.get(c.glyphIndex);
      state = new State("glyf", cg.instructions);
      if (exports_replicad.DEBUG) {
        console.log("---EXEC COMP " + i + "---");
        state.step = -1;
      }
      execComponent(cg, state, xScale, yScale);
      var dx = Math.round(c.dx * xScale);
      var dy = Math.round(c.dy * yScale);
      var gz = state.gZone;
      var cc = state.contours;
      for (var pi = 0;pi < gz.length; pi++) {
        var p = gz[pi];
        p.xTouched = p.yTouched = false;
        p.xo = p.x = p.x + dx;
        p.yo = p.y = p.y + dy;
      }
      var gLen = gZone.length;
      gZone.push.apply(gZone, gz);
      for (var j = 0;j < cc.length; j++) {
        contours.push(cc[j] + gLen);
      }
    }
    if (glyph.instructions && !state.inhibitGridFit) {
      state = new State("glyf", glyph.instructions);
      state.gZone = state.z0 = state.z1 = state.z2 = gZone;
      state.contours = contours;
      gZone.push(new HPoint(0, 0), new HPoint(Math.round(glyph.advanceWidth * xScale), 0));
      if (exports_replicad.DEBUG) {
        console.log("---EXEC COMPOSITE---");
        state.step = -1;
      }
      exec(state);
      gZone.length -= 2;
    }
  }
  return gZone;
};
execComponent = function(glyph, state, xScale, yScale) {
  var points = glyph.points || [];
  var pLen = points.length;
  var gZone = state.gZone = state.z0 = state.z1 = state.z2 = [];
  var contours = state.contours = [];
  var cp;
  for (var i = 0;i < pLen; i++) {
    cp = points[i];
    gZone[i] = new HPoint(cp.x * xScale, cp.y * yScale, cp.lastPointOfContour, cp.onCurve);
  }
  var sp;
  var np;
  for (var i$1 = 0;i$1 < pLen; i$1++) {
    cp = gZone[i$1];
    if (!sp) {
      sp = cp;
      contours.push(i$1);
    }
    if (cp.lastPointOfContour) {
      cp.nextPointOnContour = sp;
      sp.prevPointOnContour = cp;
      sp = undefined;
    } else {
      np = gZone[i$1 + 1];
      cp.nextPointOnContour = np;
      np.prevPointOnContour = cp;
    }
  }
  if (state.inhibitGridFit) {
    return;
  }
  if (exports_replicad.DEBUG) {
    console.log("PROCESSING GLYPH", state.stack);
    for (var i$2 = 0;i$2 < pLen; i$2++) {
      console.log(i$2, gZone[i$2].x, gZone[i$2].y);
    }
  }
  gZone.push(new HPoint(0, 0), new HPoint(Math.round(glyph.advanceWidth * xScale), 0));
  exec(state);
  gZone.length -= 2;
  if (exports_replicad.DEBUG) {
    console.log("FINISHED GLYPH", state.stack);
    for (var i$3 = 0;i$3 < pLen; i$3++) {
      console.log(i$3, gZone[i$3].x, gZone[i$3].y);
    }
  }
};
exec = function(state) {
  var prog = state.prog;
  if (!prog) {
    return;
  }
  var pLen = prog.length;
  var ins;
  for (state.ip = 0;state.ip < pLen; state.ip++) {
    if (exports_replicad.DEBUG) {
      state.step++;
    }
    ins = instructionTable[prog[state.ip]];
    if (!ins) {
      throw new Error("unknown instruction: 0x" + Number(prog[state.ip]).toString(16));
    }
    ins(state);
  }
};
function initTZone(state) {
  var tZone = state.tZone = new Array(state.gZone.length);
  for (var i = 0;i < tZone.length; i++) {
    tZone[i] = new HPoint(0, 0);
  }
}
function skip(state, handleElse) {
  var prog = state.prog;
  var ip = state.ip;
  var nesting = 1;
  var ins;
  do {
    ins = prog[++ip];
    if (ins === 88) {
      nesting++;
    } else if (ins === 89) {
      nesting--;
    } else if (ins === 64) {
      ip += prog[ip + 1] + 1;
    } else if (ins === 65) {
      ip += 2 * prog[ip + 1] + 1;
    } else if (ins >= 176 && ins <= 183) {
      ip += ins - 176 + 1;
    } else if (ins >= 184 && ins <= 191) {
      ip += (ins - 184 + 1) * 2;
    } else if (handleElse && nesting === 1 && ins === 27) {
      break;
    }
  } while (nesting > 0);
  state.ip = ip;
}
function SVTCA(v, state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SVTCA[" + v.axis + "]");
  }
  state.fv = state.pv = state.dpv = v;
}
function SPVTCA(v, state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SPVTCA[" + v.axis + "]");
  }
  state.pv = state.dpv = v;
}
function SFVTCA(v, state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SFVTCA[" + v.axis + "]");
  }
  state.fv = v;
}
function SPVTL(a, state) {
  var stack = state.stack;
  var p2i = stack.pop();
  var p1i = stack.pop();
  var p2 = state.z2[p2i];
  var p1 = state.z1[p1i];
  if (exports_replicad.DEBUG) {
    console.log("SPVTL[" + a + "]", p2i, p1i);
  }
  var dx;
  var dy;
  if (!a) {
    dx = p1.x - p2.x;
    dy = p1.y - p2.y;
  } else {
    dx = p2.y - p1.y;
    dy = p1.x - p2.x;
  }
  state.pv = state.dpv = getUnitVector(dx, dy);
}
function SFVTL(a, state) {
  var stack = state.stack;
  var p2i = stack.pop();
  var p1i = stack.pop();
  var p2 = state.z2[p2i];
  var p1 = state.z1[p1i];
  if (exports_replicad.DEBUG) {
    console.log("SFVTL[" + a + "]", p2i, p1i);
  }
  var dx;
  var dy;
  if (!a) {
    dx = p1.x - p2.x;
    dy = p1.y - p2.y;
  } else {
    dx = p2.y - p1.y;
    dy = p1.x - p2.x;
  }
  state.fv = getUnitVector(dx, dy);
}
function SPVFS(state) {
  var stack = state.stack;
  var y = stack.pop();
  var x = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SPVFS[]", y, x);
  }
  state.pv = state.dpv = getUnitVector(x, y);
}
function SFVFS(state) {
  var stack = state.stack;
  var y = stack.pop();
  var x = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SPVFS[]", y, x);
  }
  state.fv = getUnitVector(x, y);
}
function GPV(state) {
  var stack = state.stack;
  var pv = state.pv;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "GPV[]");
  }
  stack.push(pv.x * 16384);
  stack.push(pv.y * 16384);
}
function GFV(state) {
  var stack = state.stack;
  var fv = state.fv;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "GFV[]");
  }
  stack.push(fv.x * 16384);
  stack.push(fv.y * 16384);
}
function SFVTPV(state) {
  state.fv = state.pv;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SFVTPV[]");
  }
}
function ISECT(state) {
  var stack = state.stack;
  var pa0i = stack.pop();
  var pa1i = stack.pop();
  var pb0i = stack.pop();
  var pb1i = stack.pop();
  var pi = stack.pop();
  var z0 = state.z0;
  var z1 = state.z1;
  var pa0 = z0[pa0i];
  var pa1 = z0[pa1i];
  var pb0 = z1[pb0i];
  var pb1 = z1[pb1i];
  var p = state.z2[pi];
  if (exports_replicad.DEBUG) {
    console.log("ISECT[], ", pa0i, pa1i, pb0i, pb1i, pi);
  }
  var x1 = pa0.x;
  var y1 = pa0.y;
  var x2 = pa1.x;
  var y2 = pa1.y;
  var x3 = pb0.x;
  var y3 = pb0.y;
  var x4 = pb1.x;
  var y4 = pb1.y;
  var div = (x1 - x2) * (y3 - y4) - (y1 - y2) * (x3 - x4);
  var f1 = x1 * y2 - y1 * x2;
  var f2 = x3 * y4 - y3 * x4;
  p.x = (f1 * (x3 - x4) - f2 * (x1 - x2)) / div;
  p.y = (f1 * (y3 - y4) - f2 * (y1 - y2)) / div;
}
function SRP0(state) {
  state.rp0 = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SRP0[]", state.rp0);
  }
}
function SRP1(state) {
  state.rp1 = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SRP1[]", state.rp1);
  }
}
function SRP2(state) {
  state.rp2 = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SRP2[]", state.rp2);
  }
}
function SZP0(state) {
  var n = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SZP0[]", n);
  }
  state.zp0 = n;
  switch (n) {
    case 0:
      if (!state.tZone) {
        initTZone(state);
      }
      state.z0 = state.tZone;
      break;
    case 1:
      state.z0 = state.gZone;
      break;
    default:
      throw new Error("Invalid zone pointer");
  }
}
function SZP1(state) {
  var n = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SZP1[]", n);
  }
  state.zp1 = n;
  switch (n) {
    case 0:
      if (!state.tZone) {
        initTZone(state);
      }
      state.z1 = state.tZone;
      break;
    case 1:
      state.z1 = state.gZone;
      break;
    default:
      throw new Error("Invalid zone pointer");
  }
}
function SZP2(state) {
  var n = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SZP2[]", n);
  }
  state.zp2 = n;
  switch (n) {
    case 0:
      if (!state.tZone) {
        initTZone(state);
      }
      state.z2 = state.tZone;
      break;
    case 1:
      state.z2 = state.gZone;
      break;
    default:
      throw new Error("Invalid zone pointer");
  }
}
function SZPS(state) {
  var n = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SZPS[]", n);
  }
  state.zp0 = state.zp1 = state.zp2 = n;
  switch (n) {
    case 0:
      if (!state.tZone) {
        initTZone(state);
      }
      state.z0 = state.z1 = state.z2 = state.tZone;
      break;
    case 1:
      state.z0 = state.z1 = state.z2 = state.gZone;
      break;
    default:
      throw new Error("Invalid zone pointer");
  }
}
function SLOOP(state) {
  state.loop = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SLOOP[]", state.loop);
  }
}
function RTG(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "RTG[]");
  }
  state.round = roundToGrid;
}
function RTHG(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "RTHG[]");
  }
  state.round = roundToHalfGrid;
}
function SMD(state) {
  var d = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SMD[]", d);
  }
  state.minDis = d / 64;
}
function ELSE(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "ELSE[]");
  }
  skip(state, false);
}
function JMPR(state) {
  var o = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "JMPR[]", o);
  }
  state.ip += o - 1;
}
function SCVTCI(state) {
  var n = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SCVTCI[]", n);
  }
  state.cvCutIn = n / 64;
}
function DUP(state) {
  var stack = state.stack;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "DUP[]");
  }
  stack.push(stack[stack.length - 1]);
}
function POP(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "POP[]");
  }
  state.stack.pop();
}
function CLEAR(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "CLEAR[]");
  }
  state.stack.length = 0;
}
function SWAP(state) {
  var stack = state.stack;
  var a = stack.pop();
  var b = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SWAP[]");
  }
  stack.push(a);
  stack.push(b);
}
function DEPTH(state) {
  var stack = state.stack;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "DEPTH[]");
  }
  stack.push(stack.length);
}
function LOOPCALL(state) {
  var stack = state.stack;
  var fn = stack.pop();
  var c = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "LOOPCALL[]", fn, c);
  }
  var cip = state.ip;
  var cprog = state.prog;
  state.prog = state.funcs[fn];
  for (var i = 0;i < c; i++) {
    exec(state);
    if (exports_replicad.DEBUG) {
      console.log(++state.step, i + 1 < c ? "next loopcall" : "done loopcall", i);
    }
  }
  state.ip = cip;
  state.prog = cprog;
}
function CALL(state) {
  var fn = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "CALL[]", fn);
  }
  var cip = state.ip;
  var cprog = state.prog;
  state.prog = state.funcs[fn];
  exec(state);
  state.ip = cip;
  state.prog = cprog;
  if (exports_replicad.DEBUG) {
    console.log(++state.step, "returning from", fn);
  }
}
function CINDEX(state) {
  var stack = state.stack;
  var k = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "CINDEX[]", k);
  }
  stack.push(stack[stack.length - k]);
}
function MINDEX(state) {
  var stack = state.stack;
  var k = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "MINDEX[]", k);
  }
  stack.push(stack.splice(stack.length - k, 1)[0]);
}
function FDEF(state) {
  if (state.env !== "fpgm") {
    throw new Error("FDEF not allowed here");
  }
  var stack = state.stack;
  var prog = state.prog;
  var ip = state.ip;
  var fn = stack.pop();
  var ipBegin = ip;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "FDEF[]", fn);
  }
  while (prog[++ip] !== 45) {}
  state.ip = ip;
  state.funcs[fn] = prog.slice(ipBegin + 1, ip);
}
function MDAP(round, state) {
  var pi = state.stack.pop();
  var p = state.z0[pi];
  var fv = state.fv;
  var pv = state.pv;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "MDAP[" + round + "]", pi);
  }
  var d = pv.distance(p, HPZero);
  if (round) {
    d = state.round(d);
  }
  fv.setRelative(p, HPZero, d, pv);
  fv.touch(p);
  state.rp0 = state.rp1 = pi;
}
function IUP(v, state) {
  var z2 = state.z2;
  var pLen = z2.length - 2;
  var cp;
  var pp;
  var np;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "IUP[" + v.axis + "]");
  }
  for (var i = 0;i < pLen; i++) {
    cp = z2[i];
    if (v.touched(cp)) {
      continue;
    }
    pp = cp.prevTouched(v);
    if (pp === cp) {
      continue;
    }
    np = cp.nextTouched(v);
    if (pp === np) {
      v.setRelative(cp, cp, v.distance(pp, pp, false, true), v, true);
    }
    v.interpolate(cp, pp, np, v);
  }
}
function SHP(a, state) {
  var stack = state.stack;
  var rpi = a ? state.rp1 : state.rp2;
  var rp = (a ? state.z0 : state.z1)[rpi];
  var fv = state.fv;
  var pv = state.pv;
  var loop = state.loop;
  var z2 = state.z2;
  while (loop--) {
    var pi = stack.pop();
    var p = z2[pi];
    var d = pv.distance(rp, rp, false, true);
    fv.setRelative(p, p, d, pv);
    fv.touch(p);
    if (exports_replicad.DEBUG) {
      console.log(state.step, (state.loop > 1 ? "loop " + (state.loop - loop) + ": " : "") + "SHP[" + (a ? "rp1" : "rp2") + "]", pi);
    }
  }
  state.loop = 1;
}
function SHC(a, state) {
  var stack = state.stack;
  var rpi = a ? state.rp1 : state.rp2;
  var rp = (a ? state.z0 : state.z1)[rpi];
  var fv = state.fv;
  var pv = state.pv;
  var ci = stack.pop();
  var sp = state.z2[state.contours[ci]];
  var p = sp;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SHC[" + a + "]", ci);
  }
  var d = pv.distance(rp, rp, false, true);
  do {
    if (p !== rp) {
      fv.setRelative(p, p, d, pv);
    }
    p = p.nextPointOnContour;
  } while (p !== sp);
}
function SHZ(a, state) {
  var stack = state.stack;
  var rpi = a ? state.rp1 : state.rp2;
  var rp = (a ? state.z0 : state.z1)[rpi];
  var fv = state.fv;
  var pv = state.pv;
  var e = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SHZ[" + a + "]", e);
  }
  var z;
  switch (e) {
    case 0:
      z = state.tZone;
      break;
    case 1:
      z = state.gZone;
      break;
    default:
      throw new Error("Invalid zone");
  }
  var p;
  var d = pv.distance(rp, rp, false, true);
  var pLen = z.length - 2;
  for (var i = 0;i < pLen; i++) {
    p = z[i];
    fv.setRelative(p, p, d, pv);
  }
}
function SHPIX(state) {
  var stack = state.stack;
  var loop = state.loop;
  var fv = state.fv;
  var d = stack.pop() / 64;
  var z2 = state.z2;
  while (loop--) {
    var pi = stack.pop();
    var p = z2[pi];
    if (exports_replicad.DEBUG) {
      console.log(state.step, (state.loop > 1 ? "loop " + (state.loop - loop) + ": " : "") + "SHPIX[]", pi, d);
    }
    fv.setRelative(p, p, d);
    fv.touch(p);
  }
  state.loop = 1;
}
function IP(state) {
  var stack = state.stack;
  var rp1i = state.rp1;
  var rp2i = state.rp2;
  var loop = state.loop;
  var rp1 = state.z0[rp1i];
  var rp2 = state.z1[rp2i];
  var fv = state.fv;
  var pv = state.dpv;
  var z2 = state.z2;
  while (loop--) {
    var pi = stack.pop();
    var p = z2[pi];
    if (exports_replicad.DEBUG) {
      console.log(state.step, (state.loop > 1 ? "loop " + (state.loop - loop) + ": " : "") + "IP[]", pi, rp1i, "<->", rp2i);
    }
    fv.interpolate(p, rp1, rp2, pv);
    fv.touch(p);
  }
  state.loop = 1;
}
function MSIRP(a, state) {
  var stack = state.stack;
  var d = stack.pop() / 64;
  var pi = stack.pop();
  var p = state.z1[pi];
  var rp0 = state.z0[state.rp0];
  var fv = state.fv;
  var pv = state.pv;
  fv.setRelative(p, rp0, d, pv);
  fv.touch(p);
  if (exports_replicad.DEBUG) {
    console.log(state.step, "MSIRP[" + a + "]", d, pi);
  }
  state.rp1 = state.rp0;
  state.rp2 = pi;
  if (a) {
    state.rp0 = pi;
  }
}
function ALIGNRP(state) {
  var stack = state.stack;
  var rp0i = state.rp0;
  var rp0 = state.z0[rp0i];
  var loop = state.loop;
  var fv = state.fv;
  var pv = state.pv;
  var z1 = state.z1;
  while (loop--) {
    var pi = stack.pop();
    var p = z1[pi];
    if (exports_replicad.DEBUG) {
      console.log(state.step, (state.loop > 1 ? "loop " + (state.loop - loop) + ": " : "") + "ALIGNRP[]", pi);
    }
    fv.setRelative(p, rp0, 0, pv);
    fv.touch(p);
  }
  state.loop = 1;
}
function RTDG(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "RTDG[]");
  }
  state.round = roundToDoubleGrid;
}
function MIAP(round, state) {
  var stack = state.stack;
  var n = stack.pop();
  var pi = stack.pop();
  var p = state.z0[pi];
  var fv = state.fv;
  var pv = state.pv;
  var cv = state.cvt[n];
  if (exports_replicad.DEBUG) {
    console.log(state.step, "MIAP[" + round + "]", n, "(", cv, ")", pi);
  }
  var d = pv.distance(p, HPZero);
  if (round) {
    if (Math.abs(d - cv) < state.cvCutIn) {
      d = cv;
    }
    d = state.round(d);
  }
  fv.setRelative(p, HPZero, d, pv);
  if (state.zp0 === 0) {
    p.xo = p.x;
    p.yo = p.y;
  }
  fv.touch(p);
  state.rp0 = state.rp1 = pi;
}
function NPUSHB(state) {
  var prog = state.prog;
  var ip = state.ip;
  var stack = state.stack;
  var n = prog[++ip];
  if (exports_replicad.DEBUG) {
    console.log(state.step, "NPUSHB[]", n);
  }
  for (var i = 0;i < n; i++) {
    stack.push(prog[++ip]);
  }
  state.ip = ip;
}
function NPUSHW(state) {
  var ip = state.ip;
  var prog = state.prog;
  var stack = state.stack;
  var n = prog[++ip];
  if (exports_replicad.DEBUG) {
    console.log(state.step, "NPUSHW[]", n);
  }
  for (var i = 0;i < n; i++) {
    var w = prog[++ip] << 8 | prog[++ip];
    if (w & 32768) {
      w = -((w ^ 65535) + 1);
    }
    stack.push(w);
  }
  state.ip = ip;
}
function WS(state) {
  var stack = state.stack;
  var store = state.store;
  if (!store) {
    store = state.store = [];
  }
  var v = stack.pop();
  var l = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "WS", v, l);
  }
  store[l] = v;
}
function RS(state) {
  var stack = state.stack;
  var store = state.store;
  var l = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "RS", l);
  }
  var v = store && store[l] || 0;
  stack.push(v);
}
function WCVTP(state) {
  var stack = state.stack;
  var v = stack.pop();
  var l = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "WCVTP", v, l);
  }
  state.cvt[l] = v / 64;
}
function RCVT(state) {
  var stack = state.stack;
  var cvte = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "RCVT", cvte);
  }
  stack.push(state.cvt[cvte] * 64);
}
function GC(a, state) {
  var stack = state.stack;
  var pi = stack.pop();
  var p = state.z2[pi];
  if (exports_replicad.DEBUG) {
    console.log(state.step, "GC[" + a + "]", pi);
  }
  stack.push(state.dpv.distance(p, HPZero, a, false) * 64);
}
function MD(a, state) {
  var stack = state.stack;
  var pi2 = stack.pop();
  var pi1 = stack.pop();
  var p2 = state.z1[pi2];
  var p1 = state.z0[pi1];
  var d = state.dpv.distance(p1, p2, a, a);
  if (exports_replicad.DEBUG) {
    console.log(state.step, "MD[" + a + "]", pi2, pi1, "->", d);
  }
  state.stack.push(Math.round(d * 64));
}
function MPPEM(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "MPPEM[]");
  }
  state.stack.push(state.ppem);
}
function FLIPON(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "FLIPON[]");
  }
  state.autoFlip = true;
}
function LT(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "LT[]", e2, e1);
  }
  stack.push(e1 < e2 ? 1 : 0);
}
function LTEQ(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "LTEQ[]", e2, e1);
  }
  stack.push(e1 <= e2 ? 1 : 0);
}
function GT(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "GT[]", e2, e1);
  }
  stack.push(e1 > e2 ? 1 : 0);
}
function GTEQ(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "GTEQ[]", e2, e1);
  }
  stack.push(e1 >= e2 ? 1 : 0);
}
function EQ(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "EQ[]", e2, e1);
  }
  stack.push(e2 === e1 ? 1 : 0);
}
function NEQ(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "NEQ[]", e2, e1);
  }
  stack.push(e2 !== e1 ? 1 : 0);
}
function ODD(state) {
  var stack = state.stack;
  var n = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "ODD[]", n);
  }
  stack.push(Math.trunc(n) % 2 ? 1 : 0);
}
function EVEN(state) {
  var stack = state.stack;
  var n = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "EVEN[]", n);
  }
  stack.push(Math.trunc(n) % 2 ? 0 : 1);
}
function IF(state) {
  var test = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "IF[]", test);
  }
  if (!test) {
    skip(state, true);
    if (exports_replicad.DEBUG) {
      console.log(state.step, "EIF[]");
    }
  }
}
function EIF(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "EIF[]");
  }
}
function AND(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "AND[]", e2, e1);
  }
  stack.push(e2 && e1 ? 1 : 0);
}
function OR(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "OR[]", e2, e1);
  }
  stack.push(e2 || e1 ? 1 : 0);
}
function NOT(state) {
  var stack = state.stack;
  var e = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "NOT[]", e);
  }
  stack.push(e ? 0 : 1);
}
function DELTAP123(b, state) {
  var stack = state.stack;
  var n = stack.pop();
  var fv = state.fv;
  var pv = state.pv;
  var ppem = state.ppem;
  var base = state.deltaBase + (b - 1) * 16;
  var ds = state.deltaShift;
  var z0 = state.z0;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "DELTAP[" + b + "]", n, stack);
  }
  for (var i = 0;i < n; i++) {
    var pi = stack.pop();
    var arg = stack.pop();
    var appem = base + ((arg & 240) >> 4);
    if (appem !== ppem) {
      continue;
    }
    var mag = (arg & 15) - 8;
    if (mag >= 0) {
      mag++;
    }
    if (exports_replicad.DEBUG) {
      console.log(state.step, "DELTAPFIX", pi, "by", mag * ds);
    }
    var p = z0[pi];
    fv.setRelative(p, p, mag * ds, pv);
  }
}
function SDB(state) {
  var stack = state.stack;
  var n = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SDB[]", n);
  }
  state.deltaBase = n;
}
function SDS(state) {
  var stack = state.stack;
  var n = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SDS[]", n);
  }
  state.deltaShift = Math.pow(0.5, n);
}
function ADD(state) {
  var stack = state.stack;
  var n2 = stack.pop();
  var n1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "ADD[]", n2, n1);
  }
  stack.push(n1 + n2);
}
function SUB(state) {
  var stack = state.stack;
  var n2 = stack.pop();
  var n1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SUB[]", n2, n1);
  }
  stack.push(n1 - n2);
}
function DIV(state) {
  var stack = state.stack;
  var n2 = stack.pop();
  var n1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "DIV[]", n2, n1);
  }
  stack.push(n1 * 64 / n2);
}
function MUL(state) {
  var stack = state.stack;
  var n2 = stack.pop();
  var n1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "MUL[]", n2, n1);
  }
  stack.push(n1 * n2 / 64);
}
function ABS(state) {
  var stack = state.stack;
  var n = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "ABS[]", n);
  }
  stack.push(Math.abs(n));
}
function NEG(state) {
  var stack = state.stack;
  var n = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "NEG[]", n);
  }
  stack.push(-n);
}
function FLOOR(state) {
  var stack = state.stack;
  var n = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "FLOOR[]", n);
  }
  stack.push(Math.floor(n / 64) * 64);
}
function CEILING(state) {
  var stack = state.stack;
  var n = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "CEILING[]", n);
  }
  stack.push(Math.ceil(n / 64) * 64);
}
function ROUND(dt, state) {
  var stack = state.stack;
  var n = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "ROUND[]");
  }
  stack.push(state.round(n / 64) * 64);
}
function WCVTF(state) {
  var stack = state.stack;
  var v = stack.pop();
  var l = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "WCVTF[]", v, l);
  }
  state.cvt[l] = v * state.ppem / state.font.unitsPerEm;
}
function DELTAC123(b, state) {
  var stack = state.stack;
  var n = stack.pop();
  var ppem = state.ppem;
  var base = state.deltaBase + (b - 1) * 16;
  var ds = state.deltaShift;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "DELTAC[" + b + "]", n, stack);
  }
  for (var i = 0;i < n; i++) {
    var c = stack.pop();
    var arg = stack.pop();
    var appem = base + ((arg & 240) >> 4);
    if (appem !== ppem) {
      continue;
    }
    var mag = (arg & 15) - 8;
    if (mag >= 0) {
      mag++;
    }
    var delta = mag * ds;
    if (exports_replicad.DEBUG) {
      console.log(state.step, "DELTACFIX", c, "by", delta);
    }
    state.cvt[c] += delta;
  }
}
function SROUND(state) {
  var n = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SROUND[]", n);
  }
  state.round = roundSuper;
  var period;
  switch (n & 192) {
    case 0:
      period = 0.5;
      break;
    case 64:
      period = 1;
      break;
    case 128:
      period = 2;
      break;
    default:
      throw new Error("invalid SROUND value");
  }
  state.srPeriod = period;
  switch (n & 48) {
    case 0:
      state.srPhase = 0;
      break;
    case 16:
      state.srPhase = 0.25 * period;
      break;
    case 32:
      state.srPhase = 0.5 * period;
      break;
    case 48:
      state.srPhase = 0.75 * period;
      break;
    default:
      throw new Error("invalid SROUND value");
  }
  n &= 15;
  if (n === 0) {
    state.srThreshold = 0;
  } else {
    state.srThreshold = (n / 8 - 0.5) * period;
  }
}
function S45ROUND(state) {
  var n = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "S45ROUND[]", n);
  }
  state.round = roundSuper;
  var period;
  switch (n & 192) {
    case 0:
      period = Math.sqrt(2) / 2;
      break;
    case 64:
      period = Math.sqrt(2);
      break;
    case 128:
      period = 2 * Math.sqrt(2);
      break;
    default:
      throw new Error("invalid S45ROUND value");
  }
  state.srPeriod = period;
  switch (n & 48) {
    case 0:
      state.srPhase = 0;
      break;
    case 16:
      state.srPhase = 0.25 * period;
      break;
    case 32:
      state.srPhase = 0.5 * period;
      break;
    case 48:
      state.srPhase = 0.75 * period;
      break;
    default:
      throw new Error("invalid S45ROUND value");
  }
  n &= 15;
  if (n === 0) {
    state.srThreshold = 0;
  } else {
    state.srThreshold = (n / 8 - 0.5) * period;
  }
}
function ROFF(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "ROFF[]");
  }
  state.round = roundOff;
}
function RUTG(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "RUTG[]");
  }
  state.round = roundUpToGrid;
}
function RDTG(state) {
  if (exports_replicad.DEBUG) {
    console.log(state.step, "RDTG[]");
  }
  state.round = roundDownToGrid;
}
function SCANCTRL(state) {
  var n = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SCANCTRL[]", n);
  }
}
function SDPVTL(a, state) {
  var stack = state.stack;
  var p2i = stack.pop();
  var p1i = stack.pop();
  var p2 = state.z2[p2i];
  var p1 = state.z1[p1i];
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SDPVTL[" + a + "]", p2i, p1i);
  }
  var dx;
  var dy;
  if (!a) {
    dx = p1.x - p2.x;
    dy = p1.y - p2.y;
  } else {
    dx = p2.y - p1.y;
    dy = p1.x - p2.x;
  }
  state.dpv = getUnitVector(dx, dy);
}
function GETINFO(state) {
  var stack = state.stack;
  var sel = stack.pop();
  var r = 0;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "GETINFO[]", sel);
  }
  if (sel & 1) {
    r = 35;
  }
  if (sel & 32) {
    r |= 4096;
  }
  stack.push(r);
}
function ROLL(state) {
  var stack = state.stack;
  var a = stack.pop();
  var b = stack.pop();
  var c = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "ROLL[]");
  }
  stack.push(b);
  stack.push(a);
  stack.push(c);
}
function MAX(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "MAX[]", e2, e1);
  }
  stack.push(Math.max(e1, e2));
}
function MIN(state) {
  var stack = state.stack;
  var e2 = stack.pop();
  var e1 = stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "MIN[]", e2, e1);
  }
  stack.push(Math.min(e1, e2));
}
function SCANTYPE(state) {
  var n = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "SCANTYPE[]", n);
  }
}
function INSTCTRL(state) {
  var s = state.stack.pop();
  var v = state.stack.pop();
  if (exports_replicad.DEBUG) {
    console.log(state.step, "INSTCTRL[]", s, v);
  }
  switch (s) {
    case 1:
      state.inhibitGridFit = !!v;
      return;
    case 2:
      state.ignoreCvt = !!v;
      return;
    default:
      throw new Error("invalid INSTCTRL[] selector");
  }
}
function PUSHB(n, state) {
  var stack = state.stack;
  var prog = state.prog;
  var ip = state.ip;
  if (exports_replicad.DEBUG) {
    console.log(state.step, "PUSHB[" + n + "]");
  }
  for (var i = 0;i < n; i++) {
    stack.push(prog[++ip]);
  }
  state.ip = ip;
}
function PUSHW(n, state) {
  var ip = state.ip;
  var prog = state.prog;
  var stack = state.stack;
  if (exports_replicad.DEBUG) {
    console.log(state.ip, "PUSHW[" + n + "]");
  }
  for (var i = 0;i < n; i++) {
    var w = prog[++ip] << 8 | prog[++ip];
    if (w & 32768) {
      w = -((w ^ 65535) + 1);
    }
    stack.push(w);
  }
  state.ip = ip;
}
function MDRP_MIRP(indirect, setRp0, keepD, ro, dt, state) {
  var stack = state.stack;
  var cvte = indirect && stack.pop();
  var pi = stack.pop();
  var rp0i = state.rp0;
  var rp = state.z0[rp0i];
  var p = state.z1[pi];
  var md = state.minDis;
  var fv = state.fv;
  var pv = state.dpv;
  var od;
  var d;
  var sign;
  var cv;
  d = od = pv.distance(p, rp, true, true);
  sign = d >= 0 ? 1 : -1;
  d = Math.abs(d);
  if (indirect) {
    cv = state.cvt[cvte];
    if (ro && Math.abs(d - cv) < state.cvCutIn) {
      d = cv;
    }
  }
  if (keepD && d < md) {
    d = md;
  }
  if (ro) {
    d = state.round(d);
  }
  fv.setRelative(p, rp, sign * d, pv);
  fv.touch(p);
  if (exports_replicad.DEBUG) {
    console.log(state.step, (indirect ? "MIRP[" : "MDRP[") + (setRp0 ? "M" : "m") + (keepD ? ">" : "_") + (ro ? "R" : "_") + (dt === 0 ? "Gr" : dt === 1 ? "Bl" : dt === 2 ? "Wh" : "") + "]", indirect ? cvte + "(" + state.cvt[cvte] + "," + cv + ")" : "", pi, "(d =", od, "->", sign * d, ")");
  }
  state.rp1 = state.rp0;
  state.rp2 = pi;
  if (setRp0) {
    state.rp0 = pi;
  }
}
instructionTable = [
  SVTCA.bind(undefined, yUnitVector),
  SVTCA.bind(undefined, xUnitVector),
  SPVTCA.bind(undefined, yUnitVector),
  SPVTCA.bind(undefined, xUnitVector),
  SFVTCA.bind(undefined, yUnitVector),
  SFVTCA.bind(undefined, xUnitVector),
  SPVTL.bind(undefined, 0),
  SPVTL.bind(undefined, 1),
  SFVTL.bind(undefined, 0),
  SFVTL.bind(undefined, 1),
  SPVFS,
  SFVFS,
  GPV,
  GFV,
  SFVTPV,
  ISECT,
  SRP0,
  SRP1,
  SRP2,
  SZP0,
  SZP1,
  SZP2,
  SZPS,
  SLOOP,
  RTG,
  RTHG,
  SMD,
  ELSE,
  JMPR,
  SCVTCI,
  undefined,
  undefined,
  DUP,
  POP,
  CLEAR,
  SWAP,
  DEPTH,
  CINDEX,
  MINDEX,
  undefined,
  undefined,
  undefined,
  LOOPCALL,
  CALL,
  FDEF,
  undefined,
  MDAP.bind(undefined, 0),
  MDAP.bind(undefined, 1),
  IUP.bind(undefined, yUnitVector),
  IUP.bind(undefined, xUnitVector),
  SHP.bind(undefined, 0),
  SHP.bind(undefined, 1),
  SHC.bind(undefined, 0),
  SHC.bind(undefined, 1),
  SHZ.bind(undefined, 0),
  SHZ.bind(undefined, 1),
  SHPIX,
  IP,
  MSIRP.bind(undefined, 0),
  MSIRP.bind(undefined, 1),
  ALIGNRP,
  RTDG,
  MIAP.bind(undefined, 0),
  MIAP.bind(undefined, 1),
  NPUSHB,
  NPUSHW,
  WS,
  RS,
  WCVTP,
  RCVT,
  GC.bind(undefined, 0),
  GC.bind(undefined, 1),
  undefined,
  MD.bind(undefined, 0),
  MD.bind(undefined, 1),
  MPPEM,
  undefined,
  FLIPON,
  undefined,
  undefined,
  LT,
  LTEQ,
  GT,
  GTEQ,
  EQ,
  NEQ,
  ODD,
  EVEN,
  IF,
  EIF,
  AND,
  OR,
  NOT,
  DELTAP123.bind(undefined, 1),
  SDB,
  SDS,
  ADD,
  SUB,
  DIV,
  MUL,
  ABS,
  NEG,
  FLOOR,
  CEILING,
  ROUND.bind(undefined, 0),
  ROUND.bind(undefined, 1),
  ROUND.bind(undefined, 2),
  ROUND.bind(undefined, 3),
  undefined,
  undefined,
  undefined,
  undefined,
  WCVTF,
  DELTAP123.bind(undefined, 2),
  DELTAP123.bind(undefined, 3),
  DELTAC123.bind(undefined, 1),
  DELTAC123.bind(undefined, 2),
  DELTAC123.bind(undefined, 3),
  SROUND,
  S45ROUND,
  undefined,
  undefined,
  ROFF,
  undefined,
  RUTG,
  RDTG,
  POP,
  POP,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  SCANCTRL,
  SDPVTL.bind(undefined, 0),
  SDPVTL.bind(undefined, 1),
  GETINFO,
  undefined,
  ROLL,
  MAX,
  MIN,
  SCANTYPE,
  INSTCTRL,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  undefined,
  PUSHB.bind(undefined, 1),
  PUSHB.bind(undefined, 2),
  PUSHB.bind(undefined, 3),
  PUSHB.bind(undefined, 4),
  PUSHB.bind(undefined, 5),
  PUSHB.bind(undefined, 6),
  PUSHB.bind(undefined, 7),
  PUSHB.bind(undefined, 8),
  PUSHW.bind(undefined, 1),
  PUSHW.bind(undefined, 2),
  PUSHW.bind(undefined, 3),
  PUSHW.bind(undefined, 4),
  PUSHW.bind(undefined, 5),
  PUSHW.bind(undefined, 6),
  PUSHW.bind(undefined, 7),
  PUSHW.bind(undefined, 8),
  MDRP_MIRP.bind(undefined, 0, 0, 0, 0, 0),
  MDRP_MIRP.bind(undefined, 0, 0, 0, 0, 1),
  MDRP_MIRP.bind(undefined, 0, 0, 0, 0, 2),
  MDRP_MIRP.bind(undefined, 0, 0, 0, 0, 3),
  MDRP_MIRP.bind(undefined, 0, 0, 0, 1, 0),
  MDRP_MIRP.bind(undefined, 0, 0, 0, 1, 1),
  MDRP_MIRP.bind(undefined, 0, 0, 0, 1, 2),
  MDRP_MIRP.bind(undefined, 0, 0, 0, 1, 3),
  MDRP_MIRP.bind(undefined, 0, 0, 1, 0, 0),
  MDRP_MIRP.bind(undefined, 0, 0, 1, 0, 1),
  MDRP_MIRP.bind(undefined, 0, 0, 1, 0, 2),
  MDRP_MIRP.bind(undefined, 0, 0, 1, 0, 3),
  MDRP_MIRP.bind(undefined, 0, 0, 1, 1, 0),
  MDRP_MIRP.bind(undefined, 0, 0, 1, 1, 1),
  MDRP_MIRP.bind(undefined, 0, 0, 1, 1, 2),
  MDRP_MIRP.bind(undefined, 0, 0, 1, 1, 3),
  MDRP_MIRP.bind(undefined, 0, 1, 0, 0, 0),
  MDRP_MIRP.bind(undefined, 0, 1, 0, 0, 1),
  MDRP_MIRP.bind(undefined, 0, 1, 0, 0, 2),
  MDRP_MIRP.bind(undefined, 0, 1, 0, 0, 3),
  MDRP_MIRP.bind(undefined, 0, 1, 0, 1, 0),
  MDRP_MIRP.bind(undefined, 0, 1, 0, 1, 1),
  MDRP_MIRP.bind(undefined, 0, 1, 0, 1, 2),
  MDRP_MIRP.bind(undefined, 0, 1, 0, 1, 3),
  MDRP_MIRP.bind(undefined, 0, 1, 1, 0, 0),
  MDRP_MIRP.bind(undefined, 0, 1, 1, 0, 1),
  MDRP_MIRP.bind(undefined, 0, 1, 1, 0, 2),
  MDRP_MIRP.bind(undefined, 0, 1, 1, 0, 3),
  MDRP_MIRP.bind(undefined, 0, 1, 1, 1, 0),
  MDRP_MIRP.bind(undefined, 0, 1, 1, 1, 1),
  MDRP_MIRP.bind(undefined, 0, 1, 1, 1, 2),
  MDRP_MIRP.bind(undefined, 0, 1, 1, 1, 3),
  MDRP_MIRP.bind(undefined, 1, 0, 0, 0, 0),
  MDRP_MIRP.bind(undefined, 1, 0, 0, 0, 1),
  MDRP_MIRP.bind(undefined, 1, 0, 0, 0, 2),
  MDRP_MIRP.bind(undefined, 1, 0, 0, 0, 3),
  MDRP_MIRP.bind(undefined, 1, 0, 0, 1, 0),
  MDRP_MIRP.bind(undefined, 1, 0, 0, 1, 1),
  MDRP_MIRP.bind(undefined, 1, 0, 0, 1, 2),
  MDRP_MIRP.bind(undefined, 1, 0, 0, 1, 3),
  MDRP_MIRP.bind(undefined, 1, 0, 1, 0, 0),
  MDRP_MIRP.bind(undefined, 1, 0, 1, 0, 1),
  MDRP_MIRP.bind(undefined, 1, 0, 1, 0, 2),
  MDRP_MIRP.bind(undefined, 1, 0, 1, 0, 3),
  MDRP_MIRP.bind(undefined, 1, 0, 1, 1, 0),
  MDRP_MIRP.bind(undefined, 1, 0, 1, 1, 1),
  MDRP_MIRP.bind(undefined, 1, 0, 1, 1, 2),
  MDRP_MIRP.bind(undefined, 1, 0, 1, 1, 3),
  MDRP_MIRP.bind(undefined, 1, 1, 0, 0, 0),
  MDRP_MIRP.bind(undefined, 1, 1, 0, 0, 1),
  MDRP_MIRP.bind(undefined, 1, 1, 0, 0, 2),
  MDRP_MIRP.bind(undefined, 1, 1, 0, 0, 3),
  MDRP_MIRP.bind(undefined, 1, 1, 0, 1, 0),
  MDRP_MIRP.bind(undefined, 1, 1, 0, 1, 1),
  MDRP_MIRP.bind(undefined, 1, 1, 0, 1, 2),
  MDRP_MIRP.bind(undefined, 1, 1, 0, 1, 3),
  MDRP_MIRP.bind(undefined, 1, 1, 1, 0, 0),
  MDRP_MIRP.bind(undefined, 1, 1, 1, 0, 1),
  MDRP_MIRP.bind(undefined, 1, 1, 1, 0, 2),
  MDRP_MIRP.bind(undefined, 1, 1, 1, 0, 3),
  MDRP_MIRP.bind(undefined, 1, 1, 1, 1, 0),
  MDRP_MIRP.bind(undefined, 1, 1, 1, 1, 1),
  MDRP_MIRP.bind(undefined, 1, 1, 1, 1, 2),
  MDRP_MIRP.bind(undefined, 1, 1, 1, 1, 3)
];
function Token(char) {
  this.char = char;
  this.state = {};
  this.activeState = null;
}
function ContextRange(startIndex, endOffset, contextName) {
  this.contextName = contextName;
  this.startIndex = startIndex;
  this.endOffset = endOffset;
}
function ContextChecker(contextName, checkStart, checkEnd) {
  this.contextName = contextName;
  this.openRange = null;
  this.ranges = [];
  this.checkStart = checkStart;
  this.checkEnd = checkEnd;
}
function ContextParams(context, currentIndex) {
  this.context = context;
  this.index = currentIndex;
  this.length = context.length;
  this.current = context[currentIndex];
  this.backtrack = context.slice(0, currentIndex);
  this.lookahead = context.slice(currentIndex + 1);
}
function Event(eventId) {
  this.eventId = eventId;
  this.subscribers = [];
}
function initializeCoreEvents(events) {
  var this$1$1 = this;
  var coreEvents = [
    "start",
    "end",
    "next",
    "newToken",
    "contextStart",
    "contextEnd",
    "insertToken",
    "removeToken",
    "removeRange",
    "replaceToken",
    "replaceRange",
    "composeRUD",
    "updateContextsRanges"
  ];
  coreEvents.forEach(function(eventId) {
    Object.defineProperty(this$1$1.events, eventId, {
      value: new Event(eventId)
    });
  });
  if (!!events) {
    coreEvents.forEach(function(eventId) {
      var event = events[eventId];
      if (typeof event === "function") {
        this$1$1.events[eventId].subscribe(event);
      }
    });
  }
  var requiresContextUpdate = [
    "insertToken",
    "removeToken",
    "removeRange",
    "replaceToken",
    "replaceRange",
    "composeRUD"
  ];
  requiresContextUpdate.forEach(function(eventId) {
    this$1$1.events[eventId].subscribe(this$1$1.updateContextsRanges);
  });
}
function Tokenizer(events) {
  this.tokens = [];
  this.registeredContexts = {};
  this.contextCheckers = [];
  this.events = {};
  this.registeredModifiers = [];
  initializeCoreEvents.call(this, events);
}
Token.prototype.setState = function(key, value) {
  this.state[key] = value;
  this.activeState = { key, value: this.state[key] };
  return this.activeState;
};
Token.prototype.getState = function(stateId) {
  return this.state[stateId] || null;
};
Tokenizer.prototype.inboundIndex = function(index) {
  return index >= 0 && index < this.tokens.length;
};
Tokenizer.prototype.composeRUD = function(RUDs) {
  var this$1$1 = this;
  var silent = true;
  var state = RUDs.map(function(RUD) {
    return this$1$1[RUD[0]].apply(this$1$1, RUD.slice(1).concat(silent));
  });
  var hasFAILObject = function(obj) {
    return typeof obj === "object" && obj.hasOwnProperty("FAIL");
  };
  if (state.every(hasFAILObject)) {
    return {
      FAIL: "composeRUD: one or more operations hasn't completed successfully",
      report: state.filter(hasFAILObject)
    };
  }
  this.dispatch("composeRUD", [state.filter(function(op) {
    return !hasFAILObject(op);
  })]);
};
Tokenizer.prototype.replaceRange = function(startIndex, offset2, tokens, silent) {
  offset2 = offset2 !== null ? offset2 : this.tokens.length;
  var isTokenType = tokens.every(function(token) {
    return token instanceof Token;
  });
  if (!isNaN(startIndex) && this.inboundIndex(startIndex) && isTokenType) {
    var replaced = this.tokens.splice.apply(this.tokens, [startIndex, offset2].concat(tokens));
    if (!silent) {
      this.dispatch("replaceToken", [startIndex, offset2, tokens]);
    }
    return [replaced, tokens];
  } else {
    return { FAIL: "replaceRange: invalid tokens or startIndex." };
  }
};
Tokenizer.prototype.replaceToken = function(index, token, silent) {
  if (!isNaN(index) && this.inboundIndex(index) && token instanceof Token) {
    var replaced = this.tokens.splice(index, 1, token);
    if (!silent) {
      this.dispatch("replaceToken", [index, token]);
    }
    return [replaced[0], token];
  } else {
    return { FAIL: "replaceToken: invalid token or index." };
  }
};
Tokenizer.prototype.removeRange = function(startIndex, offset2, silent) {
  offset2 = !isNaN(offset2) ? offset2 : this.tokens.length;
  var tokens = this.tokens.splice(startIndex, offset2);
  if (!silent) {
    this.dispatch("removeRange", [tokens, startIndex, offset2]);
  }
  return tokens;
};
Tokenizer.prototype.removeToken = function(index, silent) {
  if (!isNaN(index) && this.inboundIndex(index)) {
    var token = this.tokens.splice(index, 1);
    if (!silent) {
      this.dispatch("removeToken", [token, index]);
    }
    return token;
  } else {
    return { FAIL: "removeToken: invalid token index." };
  }
};
Tokenizer.prototype.insertToken = function(tokens, index, silent) {
  var tokenType = tokens.every(function(token) {
    return token instanceof Token;
  });
  if (tokenType) {
    this.tokens.splice.apply(this.tokens, [index, 0].concat(tokens));
    if (!silent) {
      this.dispatch("insertToken", [tokens, index]);
    }
    return tokens;
  } else {
    return { FAIL: "insertToken: invalid token(s)." };
  }
};
Tokenizer.prototype.registerModifier = function(modifierId, condition, modifier) {
  this.events.newToken.subscribe(function(token, contextParams) {
    var conditionParams = [token, contextParams];
    var canApplyModifier = condition === null || condition.apply(this, conditionParams) === true;
    var modifierParams = [token, contextParams];
    if (canApplyModifier) {
      var newStateValue = modifier.apply(this, modifierParams);
      token.setState(modifierId, newStateValue);
    }
  });
  this.registeredModifiers.push(modifierId);
};
Event.prototype.subscribe = function(eventHandler) {
  if (typeof eventHandler === "function") {
    return this.subscribers.push(eventHandler) - 1;
  } else {
    return { FAIL: "invalid '" + this.eventId + "' event handler" };
  }
};
Event.prototype.unsubscribe = function(subsId) {
  this.subscribers.splice(subsId, 1);
};
ContextParams.prototype.setCurrentIndex = function(index) {
  this.index = index;
  this.current = this.context[index];
  this.backtrack = this.context.slice(0, index);
  this.lookahead = this.context.slice(index + 1);
};
ContextParams.prototype.get = function(offset2) {
  switch (true) {
    case offset2 === 0:
      return this.current;
    case (offset2 < 0 && Math.abs(offset2) <= this.backtrack.length):
      return this.backtrack.slice(offset2)[0];
    case (offset2 > 0 && offset2 <= this.lookahead.length):
      return this.lookahead[offset2 - 1];
    default:
      return null;
  }
};
Tokenizer.prototype.rangeToText = function(range2) {
  if (range2 instanceof ContextRange) {
    return this.getRangeTokens(range2).map(function(token) {
      return token.char;
    }).join("");
  }
};
Tokenizer.prototype.getText = function() {
  return this.tokens.map(function(token) {
    return token.char;
  }).join("");
};
Tokenizer.prototype.getContext = function(contextName) {
  var context = this.registeredContexts[contextName];
  return context ? context : null;
};
Tokenizer.prototype.on = function(eventName, eventHandler) {
  var event = this.events[eventName];
  if (!!event) {
    return event.subscribe(eventHandler);
  } else {
    return null;
  }
};
Tokenizer.prototype.dispatch = function(eventName, args) {
  var this$1$1 = this;
  var event = this.events[eventName];
  if (event instanceof Event) {
    event.subscribers.forEach(function(subscriber) {
      subscriber.apply(this$1$1, args || []);
    });
  }
};
Tokenizer.prototype.registerContextChecker = function(contextName, contextStartCheck, contextEndCheck) {
  if (!!this.getContext(contextName)) {
    return {
      FAIL: "context name '" + contextName + "' is already registered."
    };
  }
  if (typeof contextStartCheck !== "function") {
    return {
      FAIL: "missing context start check."
    };
  }
  if (typeof contextEndCheck !== "function") {
    return {
      FAIL: "missing context end check."
    };
  }
  var contextCheckers = new ContextChecker(contextName, contextStartCheck, contextEndCheck);
  this.registeredContexts[contextName] = contextCheckers;
  this.contextCheckers.push(contextCheckers);
  return contextCheckers;
};
Tokenizer.prototype.getRangeTokens = function(range2) {
  var endIndex = range2.startIndex + range2.endOffset;
  return [].concat(this.tokens.slice(range2.startIndex, endIndex));
};
Tokenizer.prototype.getContextRanges = function(contextName) {
  var context = this.getContext(contextName);
  if (!!context) {
    return context.ranges;
  } else {
    return { FAIL: "context checker '" + contextName + "' is not registered." };
  }
};
Tokenizer.prototype.resetContextsRanges = function() {
  var registeredContexts = this.registeredContexts;
  for (var contextName in registeredContexts) {
    if (registeredContexts.hasOwnProperty(contextName)) {
      var context = registeredContexts[contextName];
      context.ranges = [];
    }
  }
};
Tokenizer.prototype.updateContextsRanges = function() {
  this.resetContextsRanges();
  var chars = this.tokens.map(function(token) {
    return token.char;
  });
  for (var i = 0;i < chars.length; i++) {
    var contextParams = new ContextParams(chars, i);
    this.runContextCheck(contextParams);
  }
  this.dispatch("updateContextsRanges", [this.registeredContexts]);
};
Tokenizer.prototype.setEndOffset = function(offset2, contextName) {
  var startIndex = this.getContext(contextName).openRange.startIndex;
  var range2 = new ContextRange(startIndex, offset2, contextName);
  var ranges = this.getContext(contextName).ranges;
  range2.rangeId = contextName + "." + ranges.length;
  ranges.push(range2);
  this.getContext(contextName).openRange = null;
  return range2;
};
Tokenizer.prototype.runContextCheck = function(contextParams) {
  var this$1$1 = this;
  var index = contextParams.index;
  this.contextCheckers.forEach(function(contextChecker) {
    var contextName = contextChecker.contextName;
    var openRange = this$1$1.getContext(contextName).openRange;
    if (!openRange && contextChecker.checkStart(contextParams)) {
      openRange = new ContextRange(index, null, contextName);
      this$1$1.getContext(contextName).openRange = openRange;
      this$1$1.dispatch("contextStart", [contextName, index]);
    }
    if (!!openRange && contextChecker.checkEnd(contextParams)) {
      var offset2 = index - openRange.startIndex + 1;
      var range2 = this$1$1.setEndOffset(offset2, contextName);
      this$1$1.dispatch("contextEnd", [contextName, range2]);
    }
  });
};
Tokenizer.prototype.tokenize = function(text) {
  this.tokens = [];
  this.resetContextsRanges();
  var chars = Array.from(text);
  this.dispatch("start");
  for (var i = 0;i < chars.length; i++) {
    var char = chars[i];
    var contextParams = new ContextParams(chars, i);
    this.dispatch("next", [contextParams]);
    this.runContextCheck(contextParams);
    var token = new Token(char);
    this.tokens.push(token);
    this.dispatch("newToken", [token, contextParams]);
  }
  this.dispatch("end", [this.tokens]);
  return this.tokens;
};
function isArabicChar(c) {
  return /[\u0600-\u065F\u066A-\u06D2\u06FA-\u06FF]/.test(c);
}
function isIsolatedArabicChar(char) {
  return /[\u0630\u0690\u0621\u0631\u0661\u0671\u0622\u0632\u0672\u0692\u06C2\u0623\u0673\u0693\u06C3\u0624\u0694\u06C4\u0625\u0675\u0695\u06C5\u06E5\u0676\u0696\u06C6\u0627\u0677\u0697\u06C7\u0648\u0688\u0698\u06C8\u0689\u0699\u06C9\u068A\u06CA\u066B\u068B\u06CB\u068C\u068D\u06CD\u06FD\u068E\u06EE\u06FE\u062F\u068F\u06CF\u06EF]/.test(char);
}
function isTashkeelArabicChar(char) {
  return /[\u0600-\u0605\u060C-\u060E\u0610-\u061B\u061E\u064B-\u065F\u0670\u06D6-\u06DC\u06DF-\u06E4\u06E7\u06E8\u06EA-\u06ED]/.test(char);
}
function isLatinChar(c) {
  return /[A-z]/.test(c);
}
function isWhiteSpace(c) {
  return /\s/.test(c);
}
function FeatureQuery(font) {
  this.font = font;
  this.features = {};
}
function SubstitutionAction(action) {
  this.id = action.id;
  this.tag = action.tag;
  this.substitution = action.substitution;
}
function lookupCoverage(glyphIndex, coverage) {
  if (!glyphIndex) {
    return -1;
  }
  switch (coverage.format) {
    case 1:
      return coverage.glyphs.indexOf(glyphIndex);
    case 2:
      var ranges = coverage.ranges;
      for (var i = 0;i < ranges.length; i++) {
        var range2 = ranges[i];
        if (glyphIndex >= range2.start && glyphIndex <= range2.end) {
          var offset2 = glyphIndex - range2.start;
          return range2.index + offset2;
        }
      }
      break;
    default:
      return -1;
  }
  return -1;
}
function singleSubstitutionFormat1(glyphIndex, subtable) {
  var substituteIndex = lookupCoverage(glyphIndex, subtable.coverage);
  if (substituteIndex === -1) {
    return null;
  }
  return glyphIndex + subtable.deltaGlyphId;
}
function singleSubstitutionFormat2(glyphIndex, subtable) {
  var substituteIndex = lookupCoverage(glyphIndex, subtable.coverage);
  if (substituteIndex === -1) {
    return null;
  }
  return subtable.substitute[substituteIndex];
}
function lookupCoverageList(coverageList, contextParams) {
  var lookupList = [];
  for (var i = 0;i < coverageList.length; i++) {
    var coverage = coverageList[i];
    var glyphIndex = contextParams.current;
    glyphIndex = Array.isArray(glyphIndex) ? glyphIndex[0] : glyphIndex;
    var lookupIndex = lookupCoverage(glyphIndex, coverage);
    if (lookupIndex !== -1) {
      lookupList.push(lookupIndex);
    }
  }
  if (lookupList.length !== coverageList.length) {
    return -1;
  }
  return lookupList;
}
function chainingSubstitutionFormat3(contextParams, subtable) {
  var lookupsCount = subtable.inputCoverage.length + subtable.lookaheadCoverage.length + subtable.backtrackCoverage.length;
  if (contextParams.context.length < lookupsCount) {
    return [];
  }
  var inputLookups = lookupCoverageList(subtable.inputCoverage, contextParams);
  if (inputLookups === -1) {
    return [];
  }
  var lookaheadOffset = subtable.inputCoverage.length - 1;
  if (contextParams.lookahead.length < subtable.lookaheadCoverage.length) {
    return [];
  }
  var lookaheadContext = contextParams.lookahead.slice(lookaheadOffset);
  while (lookaheadContext.length && isTashkeelArabicChar(lookaheadContext[0].char)) {
    lookaheadContext.shift();
  }
  var lookaheadParams = new ContextParams(lookaheadContext, 0);
  var lookaheadLookups = lookupCoverageList(subtable.lookaheadCoverage, lookaheadParams);
  var backtrackContext = [].concat(contextParams.backtrack);
  backtrackContext.reverse();
  while (backtrackContext.length && isTashkeelArabicChar(backtrackContext[0].char)) {
    backtrackContext.shift();
  }
  if (backtrackContext.length < subtable.backtrackCoverage.length) {
    return [];
  }
  var backtrackParams = new ContextParams(backtrackContext, 0);
  var backtrackLookups = lookupCoverageList(subtable.backtrackCoverage, backtrackParams);
  var contextRulesMatch = inputLookups.length === subtable.inputCoverage.length && lookaheadLookups.length === subtable.lookaheadCoverage.length && backtrackLookups.length === subtable.backtrackCoverage.length;
  var substitutions = [];
  if (contextRulesMatch) {
    for (var i = 0;i < subtable.lookupRecords.length; i++) {
      var lookupRecord = subtable.lookupRecords[i];
      var lookupListIndex = lookupRecord.lookupListIndex;
      var lookupTable = this.getLookupByIndex(lookupListIndex);
      for (var s = 0;s < lookupTable.subtables.length; s++) {
        var subtable$1 = lookupTable.subtables[s];
        var lookup = this.getLookupMethod(lookupTable, subtable$1);
        var substitutionType = this.getSubstitutionType(lookupTable, subtable$1);
        if (substitutionType === "12") {
          for (var n = 0;n < inputLookups.length; n++) {
            var glyphIndex = contextParams.get(n);
            var substitution = lookup(glyphIndex);
            if (substitution) {
              substitutions.push(substitution);
            }
          }
        }
      }
    }
  }
  return substitutions;
}
function ligatureSubstitutionFormat1(contextParams, subtable) {
  var glyphIndex = contextParams.current;
  var ligSetIndex = lookupCoverage(glyphIndex, subtable.coverage);
  if (ligSetIndex === -1) {
    return null;
  }
  var ligature;
  var ligatureSet = subtable.ligatureSets[ligSetIndex];
  for (var s = 0;s < ligatureSet.length; s++) {
    ligature = ligatureSet[s];
    for (var l = 0;l < ligature.components.length; l++) {
      var lookaheadItem = contextParams.lookahead[l];
      var component = ligature.components[l];
      if (lookaheadItem !== component) {
        break;
      }
      if (l === ligature.components.length - 1) {
        return ligature;
      }
    }
  }
  return null;
}
function decompositionSubstitutionFormat1(glyphIndex, subtable) {
  var substituteIndex = lookupCoverage(glyphIndex, subtable.coverage);
  if (substituteIndex === -1) {
    return null;
  }
  return subtable.sequences[substituteIndex];
}
FeatureQuery.prototype.getDefaultScriptFeaturesIndexes = function() {
  var scripts = this.font.tables.gsub.scripts;
  for (var s = 0;s < scripts.length; s++) {
    var script = scripts[s];
    if (script.tag === "DFLT") {
      return script.script.defaultLangSys.featureIndexes;
    }
  }
  return [];
};
FeatureQuery.prototype.getScriptFeaturesIndexes = function(scriptTag) {
  var tables = this.font.tables;
  if (!tables.gsub) {
    return [];
  }
  if (!scriptTag) {
    return this.getDefaultScriptFeaturesIndexes();
  }
  var scripts = this.font.tables.gsub.scripts;
  for (var i = 0;i < scripts.length; i++) {
    var script = scripts[i];
    if (script.tag === scriptTag && script.script.defaultLangSys) {
      return script.script.defaultLangSys.featureIndexes;
    } else {
      var langSysRecords = script.langSysRecords;
      if (!!langSysRecords) {
        for (var j = 0;j < langSysRecords.length; j++) {
          var langSysRecord = langSysRecords[j];
          if (langSysRecord.tag === scriptTag) {
            var langSys = langSysRecord.langSys;
            return langSys.featureIndexes;
          }
        }
      }
    }
  }
  return this.getDefaultScriptFeaturesIndexes();
};
FeatureQuery.prototype.mapTagsToFeatures = function(features, scriptTag) {
  var tags = {};
  for (var i = 0;i < features.length; i++) {
    var tag = features[i].tag;
    var feature = features[i].feature;
    tags[tag] = feature;
  }
  this.features[scriptTag].tags = tags;
};
FeatureQuery.prototype.getScriptFeatures = function(scriptTag) {
  var features = this.features[scriptTag];
  if (this.features.hasOwnProperty(scriptTag)) {
    return features;
  }
  var featuresIndexes = this.getScriptFeaturesIndexes(scriptTag);
  if (!featuresIndexes) {
    return null;
  }
  var gsub2 = this.font.tables.gsub;
  features = featuresIndexes.map(function(index) {
    return gsub2.features[index];
  });
  this.features[scriptTag] = features;
  this.mapTagsToFeatures(features, scriptTag);
  return features;
};
FeatureQuery.prototype.getSubstitutionType = function(lookupTable, subtable) {
  var lookupType = lookupTable.lookupType.toString();
  var substFormat = subtable.substFormat.toString();
  return lookupType + substFormat;
};
FeatureQuery.prototype.getLookupMethod = function(lookupTable, subtable) {
  var this$1$1 = this;
  var substitutionType = this.getSubstitutionType(lookupTable, subtable);
  switch (substitutionType) {
    case "11":
      return function(glyphIndex) {
        return singleSubstitutionFormat1.apply(this$1$1, [glyphIndex, subtable]);
      };
    case "12":
      return function(glyphIndex) {
        return singleSubstitutionFormat2.apply(this$1$1, [glyphIndex, subtable]);
      };
    case "63":
      return function(contextParams) {
        return chainingSubstitutionFormat3.apply(this$1$1, [contextParams, subtable]);
      };
    case "41":
      return function(contextParams) {
        return ligatureSubstitutionFormat1.apply(this$1$1, [contextParams, subtable]);
      };
    case "21":
      return function(glyphIndex) {
        return decompositionSubstitutionFormat1.apply(this$1$1, [glyphIndex, subtable]);
      };
    default:
      throw new Error("lookupType: " + lookupTable.lookupType + " - substFormat: " + subtable.substFormat + " is not yet supported");
  }
};
FeatureQuery.prototype.lookupFeature = function(query) {
  var contextParams = query.contextParams;
  var currentIndex = contextParams.index;
  var feature = this.getFeature({
    tag: query.tag,
    script: query.script
  });
  if (!feature) {
    return new Error("font '" + this.font.names.fullName.en + "' doesn't support feature '" + query.tag + "' for script '" + query.script + "'.");
  }
  var lookups = this.getFeatureLookups(feature);
  var substitutions = [].concat(contextParams.context);
  for (var l = 0;l < lookups.length; l++) {
    var lookupTable = lookups[l];
    var subtables = this.getLookupSubtables(lookupTable);
    for (var s = 0;s < subtables.length; s++) {
      var subtable = subtables[s];
      var substType = this.getSubstitutionType(lookupTable, subtable);
      var lookup = this.getLookupMethod(lookupTable, subtable);
      var substitution = undefined;
      switch (substType) {
        case "11":
          substitution = lookup(contextParams.current);
          if (substitution) {
            substitutions.splice(currentIndex, 1, new SubstitutionAction({
              id: 11,
              tag: query.tag,
              substitution
            }));
          }
          break;
        case "12":
          substitution = lookup(contextParams.current);
          if (substitution) {
            substitutions.splice(currentIndex, 1, new SubstitutionAction({
              id: 12,
              tag: query.tag,
              substitution
            }));
          }
          break;
        case "63":
          substitution = lookup(contextParams);
          if (Array.isArray(substitution) && substitution.length) {
            substitutions.splice(currentIndex, 1, new SubstitutionAction({
              id: 63,
              tag: query.tag,
              substitution
            }));
          }
          break;
        case "41":
          substitution = lookup(contextParams);
          if (substitution) {
            substitutions.splice(currentIndex, 1, new SubstitutionAction({
              id: 41,
              tag: query.tag,
              substitution
            }));
          }
          break;
        case "21":
          substitution = lookup(contextParams.current);
          if (substitution) {
            substitutions.splice(currentIndex, 1, new SubstitutionAction({
              id: 21,
              tag: query.tag,
              substitution
            }));
          }
          break;
      }
      contextParams = new ContextParams(substitutions, currentIndex);
      if (Array.isArray(substitution) && !substitution.length) {
        continue;
      }
      substitution = null;
    }
  }
  return substitutions.length ? substitutions : null;
};
FeatureQuery.prototype.supports = function(query) {
  if (!query.script) {
    return false;
  }
  this.getScriptFeatures(query.script);
  var supportedScript = this.features.hasOwnProperty(query.script);
  if (!query.tag) {
    return supportedScript;
  }
  var supportedFeature = this.features[query.script].some(function(feature) {
    return feature.tag === query.tag;
  });
  return supportedScript && supportedFeature;
};
FeatureQuery.prototype.getLookupSubtables = function(lookupTable) {
  return lookupTable.subtables || null;
};
FeatureQuery.prototype.getLookupByIndex = function(index) {
  var lookups = this.font.tables.gsub.lookups;
  return lookups[index] || null;
};
FeatureQuery.prototype.getFeatureLookups = function(feature) {
  return feature.lookupListIndexes.map(this.getLookupByIndex.bind(this));
};
FeatureQuery.prototype.getFeature = function getFeature(query) {
  if (!this.font) {
    return { FAIL: "No font was found" };
  }
  if (!this.features.hasOwnProperty(query.script)) {
    this.getScriptFeatures(query.script);
  }
  var scriptFeatures = this.features[query.script];
  if (!scriptFeatures) {
    return { FAIL: "No feature for script " + query.script };
  }
  if (!scriptFeatures.tags[query.tag]) {
    return null;
  }
  return this.features[query.script].tags[query.tag];
};
function arabicWordStartCheck(contextParams) {
  var char = contextParams.current;
  var prevChar = contextParams.get(-1);
  return prevChar === null && isArabicChar(char) || !isArabicChar(prevChar) && isArabicChar(char);
}
function arabicWordEndCheck(contextParams) {
  var nextChar = contextParams.get(1);
  return nextChar === null || !isArabicChar(nextChar);
}
var arabicWordCheck = {
  startCheck: arabicWordStartCheck,
  endCheck: arabicWordEndCheck
};
function arabicSentenceStartCheck(contextParams) {
  var char = contextParams.current;
  var prevChar = contextParams.get(-1);
  return (isArabicChar(char) || isTashkeelArabicChar(char)) && !isArabicChar(prevChar);
}
function arabicSentenceEndCheck(contextParams) {
  var nextChar = contextParams.get(1);
  switch (true) {
    case nextChar === null:
      return true;
    case (!isArabicChar(nextChar) && !isTashkeelArabicChar(nextChar)):
      var nextIsWhitespace = isWhiteSpace(nextChar);
      if (!nextIsWhitespace) {
        return true;
      }
      if (nextIsWhitespace) {
        var arabicCharAhead = false;
        arabicCharAhead = contextParams.lookahead.some(function(c) {
          return isArabicChar(c) || isTashkeelArabicChar(c);
        });
        if (!arabicCharAhead) {
          return true;
        }
      }
      break;
    default:
      return false;
  }
}
var arabicSentenceCheck = {
  startCheck: arabicSentenceStartCheck,
  endCheck: arabicSentenceEndCheck
};
function singleSubstitutionFormat1$1(action, tokens, index) {
  tokens[index].setState(action.tag, action.substitution);
}
function singleSubstitutionFormat2$1(action, tokens, index) {
  tokens[index].setState(action.tag, action.substitution);
}
function chainingSubstitutionFormat3$1(action, tokens, index) {
  action.substitution.forEach(function(subst, offset2) {
    var token = tokens[index + offset2];
    token.setState(action.tag, subst);
  });
}
function ligatureSubstitutionFormat1$1(action, tokens, index) {
  var token = tokens[index];
  token.setState(action.tag, action.substitution.ligGlyph);
  var compsCount = action.substitution.components.length;
  for (var i = 0;i < compsCount; i++) {
    token = tokens[index + i + 1];
    token.setState("deleted", true);
  }
}
var SUBSTITUTIONS = {
  11: singleSubstitutionFormat1$1,
  12: singleSubstitutionFormat2$1,
  63: chainingSubstitutionFormat3$1,
  41: ligatureSubstitutionFormat1$1
};
function applySubstitution(action, tokens, index) {
  if (action instanceof SubstitutionAction && SUBSTITUTIONS[action.id]) {
    SUBSTITUTIONS[action.id](action, tokens, index);
  }
}
function willConnectPrev(charContextParams) {
  var backtrack = [].concat(charContextParams.backtrack);
  for (var i = backtrack.length - 1;i >= 0; i--) {
    var prevChar = backtrack[i];
    var isolated = isIsolatedArabicChar(prevChar);
    var tashkeel = isTashkeelArabicChar(prevChar);
    if (!isolated && !tashkeel) {
      return true;
    }
    if (isolated) {
      return false;
    }
  }
  return false;
}
function willConnectNext(charContextParams) {
  if (isIsolatedArabicChar(charContextParams.current)) {
    return false;
  }
  for (var i = 0;i < charContextParams.lookahead.length; i++) {
    var nextChar = charContextParams.lookahead[i];
    var tashkeel = isTashkeelArabicChar(nextChar);
    if (!tashkeel) {
      return true;
    }
  }
  return false;
}
function arabicPresentationForms(range2) {
  var this$1$1 = this;
  var script = "arab";
  var tags = this.featuresTags[script];
  var tokens = this.tokenizer.getRangeTokens(range2);
  if (tokens.length === 1) {
    return;
  }
  var contextParams = new ContextParams(tokens.map(function(token) {
    return token.getState("glyphIndex");
  }), 0);
  var charContextParams = new ContextParams(tokens.map(function(token) {
    return token.char;
  }), 0);
  tokens.forEach(function(token, index) {
    if (isTashkeelArabicChar(token.char)) {
      return;
    }
    contextParams.setCurrentIndex(index);
    charContextParams.setCurrentIndex(index);
    var CONNECT = 0;
    if (willConnectPrev(charContextParams)) {
      CONNECT |= 1;
    }
    if (willConnectNext(charContextParams)) {
      CONNECT |= 2;
    }
    var tag;
    switch (CONNECT) {
      case 1:
        tag = "fina";
        break;
      case 2:
        tag = "init";
        break;
      case 3:
        tag = "medi";
        break;
    }
    if (tags.indexOf(tag) === -1) {
      return;
    }
    var substitutions = this$1$1.query.lookupFeature({
      tag,
      script,
      contextParams
    });
    if (substitutions instanceof Error) {
      return console.info(substitutions.message);
    }
    substitutions.forEach(function(action, index2) {
      if (action instanceof SubstitutionAction) {
        applySubstitution(action, tokens, index2);
        contextParams.context[index2] = action.substitution;
      }
    });
  });
}
function getContextParams(tokens, index) {
  var context = tokens.map(function(token) {
    return token.activeState.value;
  });
  return new ContextParams(context, index || 0);
}
function arabicRequiredLigatures(range2) {
  var this$1$1 = this;
  var script = "arab";
  var tokens = this.tokenizer.getRangeTokens(range2);
  var contextParams = getContextParams(tokens);
  contextParams.context.forEach(function(glyphIndex, index) {
    contextParams.setCurrentIndex(index);
    var substitutions = this$1$1.query.lookupFeature({
      tag: "rlig",
      script,
      contextParams
    });
    if (substitutions.length) {
      substitutions.forEach(function(action) {
        return applySubstitution(action, tokens, index);
      });
      contextParams = getContextParams(tokens);
    }
  });
}
function latinWordStartCheck(contextParams) {
  var char = contextParams.current;
  var prevChar = contextParams.get(-1);
  return prevChar === null && isLatinChar(char) || !isLatinChar(prevChar) && isLatinChar(char);
}
function latinWordEndCheck(contextParams) {
  var nextChar = contextParams.get(1);
  return nextChar === null || !isLatinChar(nextChar);
}
var latinWordCheck = {
  startCheck: latinWordStartCheck,
  endCheck: latinWordEndCheck
};
function getContextParams$1(tokens, index) {
  var context = tokens.map(function(token) {
    return token.activeState.value;
  });
  return new ContextParams(context, index || 0);
}
function latinLigature(range2) {
  var this$1$1 = this;
  var script = "latn";
  var tokens = this.tokenizer.getRangeTokens(range2);
  var contextParams = getContextParams$1(tokens);
  contextParams.context.forEach(function(glyphIndex, index) {
    contextParams.setCurrentIndex(index);
    var substitutions = this$1$1.query.lookupFeature({
      tag: "liga",
      script,
      contextParams
    });
    if (substitutions.length) {
      substitutions.forEach(function(action) {
        return applySubstitution(action, tokens, index);
      });
      contextParams = getContextParams$1(tokens);
    }
  });
}
function Bidi(baseDir) {
  this.baseDir = baseDir || "ltr";
  this.tokenizer = new Tokenizer;
  this.featuresTags = {};
}
Bidi.prototype.setText = function(text) {
  this.text = text;
};
Bidi.prototype.contextChecks = {
  latinWordCheck,
  arabicWordCheck,
  arabicSentenceCheck
};
function registerContextChecker(checkId) {
  var check2 = this.contextChecks[checkId + "Check"];
  return this.tokenizer.registerContextChecker(checkId, check2.startCheck, check2.endCheck);
}
function tokenizeText() {
  registerContextChecker.call(this, "latinWord");
  registerContextChecker.call(this, "arabicWord");
  registerContextChecker.call(this, "arabicSentence");
  return this.tokenizer.tokenize(this.text);
}
function reverseArabicSentences() {
  var this$1$1 = this;
  var ranges = this.tokenizer.getContextRanges("arabicSentence");
  ranges.forEach(function(range2) {
    var rangeTokens = this$1$1.tokenizer.getRangeTokens(range2);
    this$1$1.tokenizer.replaceRange(range2.startIndex, range2.endOffset, rangeTokens.reverse());
  });
}
Bidi.prototype.registerFeatures = function(script, tags) {
  var this$1$1 = this;
  var supportedTags = tags.filter(function(tag) {
    return this$1$1.query.supports({ script, tag });
  });
  if (!this.featuresTags.hasOwnProperty(script)) {
    this.featuresTags[script] = supportedTags;
  } else {
    this.featuresTags[script] = this.featuresTags[script].concat(supportedTags);
  }
};
Bidi.prototype.applyFeatures = function(font, features) {
  if (!font) {
    throw new Error("No valid font was provided to apply features");
  }
  if (!this.query) {
    this.query = new FeatureQuery(font);
  }
  for (var f = 0;f < features.length; f++) {
    var feature = features[f];
    if (!this.query.supports({ script: feature.script })) {
      continue;
    }
    this.registerFeatures(feature.script, feature.tags);
  }
};
Bidi.prototype.registerModifier = function(modifierId, condition, modifier) {
  this.tokenizer.registerModifier(modifierId, condition, modifier);
};
function checkGlyphIndexStatus() {
  if (this.tokenizer.registeredModifiers.indexOf("glyphIndex") === -1) {
    throw new Error("glyphIndex modifier is required to apply arabic presentation features.");
  }
}
function applyArabicPresentationForms() {
  var this$1$1 = this;
  var script = "arab";
  if (!this.featuresTags.hasOwnProperty(script)) {
    return;
  }
  checkGlyphIndexStatus.call(this);
  var ranges = this.tokenizer.getContextRanges("arabicWord");
  ranges.forEach(function(range2) {
    arabicPresentationForms.call(this$1$1, range2);
  });
}
function applyArabicRequireLigatures() {
  var this$1$1 = this;
  var script = "arab";
  if (!this.featuresTags.hasOwnProperty(script)) {
    return;
  }
  var tags = this.featuresTags[script];
  if (tags.indexOf("rlig") === -1) {
    return;
  }
  checkGlyphIndexStatus.call(this);
  var ranges = this.tokenizer.getContextRanges("arabicWord");
  ranges.forEach(function(range2) {
    arabicRequiredLigatures.call(this$1$1, range2);
  });
}
function applyLatinLigatures() {
  var this$1$1 = this;
  var script = "latn";
  if (!this.featuresTags.hasOwnProperty(script)) {
    return;
  }
  var tags = this.featuresTags[script];
  if (tags.indexOf("liga") === -1) {
    return;
  }
  checkGlyphIndexStatus.call(this);
  var ranges = this.tokenizer.getContextRanges("latinWord");
  ranges.forEach(function(range2) {
    latinLigature.call(this$1$1, range2);
  });
}
Bidi.prototype.checkContextReady = function(contextId) {
  return !!this.tokenizer.getContext(contextId);
};
Bidi.prototype.applyFeaturesToContexts = function() {
  if (this.checkContextReady("arabicWord")) {
    applyArabicPresentationForms.call(this);
    applyArabicRequireLigatures.call(this);
  }
  if (this.checkContextReady("latinWord")) {
    applyLatinLigatures.call(this);
  }
  if (this.checkContextReady("arabicSentence")) {
    reverseArabicSentences.call(this);
  }
};
Bidi.prototype.processText = function(text) {
  if (!this.text || this.text !== text) {
    this.setText(text);
    tokenizeText.call(this);
    this.applyFeaturesToContexts();
  }
};
Bidi.prototype.getBidiText = function(text) {
  this.processText(text);
  return this.tokenizer.getText();
};
Bidi.prototype.getTextGlyphs = function(text) {
  this.processText(text);
  var indexes = [];
  for (var i = 0;i < this.tokenizer.tokens.length; i++) {
    var token = this.tokenizer.tokens[i];
    if (token.state.deleted) {
      continue;
    }
    var index = token.activeState.value;
    indexes.push(Array.isArray(index) ? index[0] : index);
  }
  return indexes;
};
function Font(options) {
  options = options || {};
  options.tables = options.tables || {};
  if (!options.empty) {
    checkArgument(options.familyName, "When creating a new Font object, familyName is required.");
    checkArgument(options.styleName, "When creating a new Font object, styleName is required.");
    checkArgument(options.unitsPerEm, "When creating a new Font object, unitsPerEm is required.");
    checkArgument(options.ascender, "When creating a new Font object, ascender is required.");
    checkArgument(options.descender <= 0, "When creating a new Font object, negative descender value is required.");
    this.names = {
      fontFamily: { en: options.familyName || " " },
      fontSubfamily: { en: options.styleName || " " },
      fullName: { en: options.fullName || options.familyName + " " + options.styleName },
      postScriptName: { en: options.postScriptName || (options.familyName + options.styleName).replace(/\s/g, "") },
      designer: { en: options.designer || " " },
      designerURL: { en: options.designerURL || " " },
      manufacturer: { en: options.manufacturer || " " },
      manufacturerURL: { en: options.manufacturerURL || " " },
      license: { en: options.license || " " },
      licenseURL: { en: options.licenseURL || " " },
      version: { en: options.version || "Version 0.1" },
      description: { en: options.description || " " },
      copyright: { en: options.copyright || " " },
      trademark: { en: options.trademark || " " }
    };
    this.unitsPerEm = options.unitsPerEm || 1000;
    this.ascender = options.ascender;
    this.descender = options.descender;
    this.createdTimestamp = options.createdTimestamp;
    this.tables = Object.assign(options.tables, {
      os2: Object.assign({
        usWeightClass: options.weightClass || this.usWeightClasses.MEDIUM,
        usWidthClass: options.widthClass || this.usWidthClasses.MEDIUM,
        fsSelection: options.fsSelection || this.fsSelectionValues.REGULAR
      }, options.tables.os2)
    });
  }
  this.supported = true;
  this.glyphs = new glyphset.GlyphSet(this, options.glyphs || []);
  this.encoding = new DefaultEncoding(this);
  this.position = new Position(this);
  this.substitution = new Substitution(this);
  this.tables = this.tables || {};
  this._push = null;
  this._hmtxTableData = {};
  Object.defineProperty(this, "hinting", {
    get: function() {
      if (this._hinting) {
        return this._hinting;
      }
      if (this.outlinesFormat === "truetype") {
        return this._hinting = new Hinting(this);
      }
    }
  });
}
Font.prototype.hasChar = function(c) {
  return this.encoding.charToGlyphIndex(c) !== null;
};
Font.prototype.charToGlyphIndex = function(s) {
  return this.encoding.charToGlyphIndex(s);
};
Font.prototype.charToGlyph = function(c) {
  var glyphIndex = this.charToGlyphIndex(c);
  var glyph = this.glyphs.get(glyphIndex);
  if (!glyph) {
    glyph = this.glyphs.get(0);
  }
  return glyph;
};
Font.prototype.updateFeatures = function(options) {
  return this.defaultRenderOptions.features.map(function(feature) {
    if (feature.script === "latn") {
      return {
        script: "latn",
        tags: feature.tags.filter(function(tag) {
          return options[tag];
        })
      };
    } else {
      return feature;
    }
  });
};
Font.prototype.stringToGlyphs = function(s, options) {
  var this$1$1 = this;
  var bidi = new Bidi;
  var charToGlyphIndexMod = function(token) {
    return this$1$1.charToGlyphIndex(token.char);
  };
  bidi.registerModifier("glyphIndex", null, charToGlyphIndexMod);
  var features = options ? this.updateFeatures(options.features) : this.defaultRenderOptions.features;
  bidi.applyFeatures(this, features);
  var indexes = bidi.getTextGlyphs(s);
  var length = indexes.length;
  var glyphs = new Array(length);
  var notdef = this.glyphs.get(0);
  for (var i = 0;i < length; i += 1) {
    glyphs[i] = this.glyphs.get(indexes[i]) || notdef;
  }
  return glyphs;
};
Font.prototype.nameToGlyphIndex = function(name) {
  return this.glyphNames.nameToGlyphIndex(name);
};
Font.prototype.nameToGlyph = function(name) {
  var glyphIndex = this.nameToGlyphIndex(name);
  var glyph = this.glyphs.get(glyphIndex);
  if (!glyph) {
    glyph = this.glyphs.get(0);
  }
  return glyph;
};
Font.prototype.glyphIndexToName = function(gid) {
  if (!this.glyphNames.glyphIndexToName) {
    return "";
  }
  return this.glyphNames.glyphIndexToName(gid);
};
Font.prototype.getKerningValue = function(leftGlyph, rightGlyph) {
  leftGlyph = leftGlyph.index || leftGlyph;
  rightGlyph = rightGlyph.index || rightGlyph;
  var gposKerning = this.position.defaultKerningTables;
  if (gposKerning) {
    return this.position.getKerningValue(gposKerning, leftGlyph, rightGlyph);
  }
  return this.kerningPairs[leftGlyph + "," + rightGlyph] || 0;
};
Font.prototype.defaultRenderOptions = {
  kerning: true,
  features: [
    { script: "arab", tags: ["init", "medi", "fina", "rlig"] },
    { script: "latn", tags: ["liga", "rlig"] }
  ]
};
Font.prototype.forEachGlyph = function(text, x, y, fontSize, options, callback) {
  x = x !== undefined ? x : 0;
  y = y !== undefined ? y : 0;
  fontSize = fontSize !== undefined ? fontSize : 72;
  options = Object.assign({}, this.defaultRenderOptions, options);
  var fontScale = 1 / this.unitsPerEm * fontSize;
  var glyphs = this.stringToGlyphs(text, options);
  var kerningLookups;
  if (options.kerning) {
    var script = options.script || this.position.getDefaultScriptName();
    kerningLookups = this.position.getKerningTables(script, options.language);
  }
  for (var i = 0;i < glyphs.length; i += 1) {
    var glyph = glyphs[i];
    callback.call(this, glyph, x, y, fontSize, options);
    if (glyph.advanceWidth) {
      x += glyph.advanceWidth * fontScale;
    }
    if (options.kerning && i < glyphs.length - 1) {
      var kerningValue = kerningLookups ? this.position.getKerningValue(kerningLookups, glyph.index, glyphs[i + 1].index) : this.getKerningValue(glyph, glyphs[i + 1]);
      x += kerningValue * fontScale;
    }
    if (options.letterSpacing) {
      x += options.letterSpacing * fontSize;
    } else if (options.tracking) {
      x += options.tracking / 1000 * fontSize;
    }
  }
  return x;
};
Font.prototype.getPath = function(text, x, y, fontSize, options) {
  var fullPath = new Path;
  this.forEachGlyph(text, x, y, fontSize, options, function(glyph, gX, gY, gFontSize) {
    var glyphPath = glyph.getPath(gX, gY, gFontSize, options, this);
    fullPath.extend(glyphPath);
  });
  return fullPath;
};
Font.prototype.getPaths = function(text, x, y, fontSize, options) {
  var glyphPaths = [];
  this.forEachGlyph(text, x, y, fontSize, options, function(glyph, gX, gY, gFontSize) {
    var glyphPath = glyph.getPath(gX, gY, gFontSize, options, this);
    glyphPaths.push(glyphPath);
  });
  return glyphPaths;
};
Font.prototype.getAdvanceWidth = function(text, fontSize, options) {
  return this.forEachGlyph(text, 0, 0, fontSize, options, function() {});
};
Font.prototype.draw = function(ctx, text, x, y, fontSize, options) {
  this.getPath(text, x, y, fontSize, options).draw(ctx);
};
Font.prototype.drawPoints = function(ctx, text, x, y, fontSize, options) {
  this.forEachGlyph(text, x, y, fontSize, options, function(glyph, gX, gY, gFontSize) {
    glyph.drawPoints(ctx, gX, gY, gFontSize);
  });
};
Font.prototype.drawMetrics = function(ctx, text, x, y, fontSize, options) {
  this.forEachGlyph(text, x, y, fontSize, options, function(glyph, gX, gY, gFontSize) {
    glyph.drawMetrics(ctx, gX, gY, gFontSize);
  });
};
Font.prototype.getEnglishName = function(name) {
  var translations = this.names[name];
  if (translations) {
    return translations.en;
  }
};
Font.prototype.validate = function() {
  var _this = this;
  function assert(predicate, message) {}
  function assertNamePresent(name) {
    var englishName = _this.getEnglishName(name);
    assert(englishName && englishName.trim().length > 0);
  }
  assertNamePresent("fontFamily");
  assertNamePresent("weightName");
  assertNamePresent("manufacturer");
  assertNamePresent("copyright");
  assertNamePresent("version");
  assert(this.unitsPerEm > 0);
};
Font.prototype.toTables = function() {
  return sfnt.fontToTable(this);
};
Font.prototype.toBuffer = function() {
  console.warn("Font.toBuffer is deprecated. Use Font.toArrayBuffer instead.");
  return this.toArrayBuffer();
};
Font.prototype.toArrayBuffer = function() {
  var sfntTable = this.toTables();
  var bytes = sfntTable.encode();
  var buffer = new ArrayBuffer(bytes.length);
  var intArray = new Uint8Array(buffer);
  for (var i = 0;i < bytes.length; i++) {
    intArray[i] = bytes[i];
  }
  return buffer;
};
Font.prototype.download = function(fileName) {
  var familyName = this.getEnglishName("fontFamily");
  var styleName = this.getEnglishName("fontSubfamily");
  fileName = fileName || familyName.replace(/\s/g, "") + "-" + styleName + ".otf";
  var arrayBuffer = this.toArrayBuffer();
  if (isBrowser()) {
    window.URL = window.URL || window.webkitURL;
    if (window.URL) {
      var dataView = new DataView(arrayBuffer);
      var blob = new Blob([dataView], { type: "font/opentype" });
      var link = document.createElement("a");
      link.href = window.URL.createObjectURL(blob);
      link.download = fileName;
      var event = document.createEvent("MouseEvents");
      event.initEvent("click", true, false);
      link.dispatchEvent(event);
    } else {
      console.warn("Font file could not be downloaded. Try using a different browser.");
    }
  } else {
    var fs = __require("fs");
    var buffer = arrayBufferToNodeBuffer(arrayBuffer);
    fs.writeFileSync(fileName, buffer);
  }
};
Font.prototype.fsSelectionValues = {
  ITALIC: 1,
  UNDERSCORE: 2,
  NEGATIVE: 4,
  OUTLINED: 8,
  STRIKEOUT: 16,
  BOLD: 32,
  REGULAR: 64,
  USER_TYPO_METRICS: 128,
  WWS: 256,
  OBLIQUE: 512
};
Font.prototype.usWidthClasses = {
  ULTRA_CONDENSED: 1,
  EXTRA_CONDENSED: 2,
  CONDENSED: 3,
  SEMI_CONDENSED: 4,
  MEDIUM: 5,
  SEMI_EXPANDED: 6,
  EXPANDED: 7,
  EXTRA_EXPANDED: 8,
  ULTRA_EXPANDED: 9
};
Font.prototype.usWeightClasses = {
  THIN: 100,
  EXTRA_LIGHT: 200,
  LIGHT: 300,
  NORMAL: 400,
  MEDIUM: 500,
  SEMI_BOLD: 600,
  BOLD: 700,
  EXTRA_BOLD: 800,
  BLACK: 900
};
var subtableParsers$1 = new Array(10);
subtableParsers$1[1] = function parseLookup12() {
  var start = this.offset + this.relativeOffset;
  var posformat = this.parseUShort();
  if (posformat === 1) {
    return {
      posFormat: 1,
      coverage: this.parsePointer(Parser.coverage),
      value: this.parseValueRecord()
    };
  } else if (posformat === 2) {
    return {
      posFormat: 2,
      coverage: this.parsePointer(Parser.coverage),
      values: this.parseValueRecordList()
    };
  }
  check.assert(false, "0x" + start.toString(16) + ": GPOS lookup type 1 format must be 1 or 2.");
};
subtableParsers$1[2] = function parseLookup22() {
  var start = this.offset + this.relativeOffset;
  var posFormat = this.parseUShort();
  check.assert(posFormat === 1 || posFormat === 2, "0x" + start.toString(16) + ": GPOS lookup type 2 format must be 1 or 2.");
  var coverage = this.parsePointer(Parser.coverage);
  var valueFormat1 = this.parseUShort();
  var valueFormat2 = this.parseUShort();
  if (posFormat === 1) {
    return {
      posFormat,
      coverage,
      valueFormat1,
      valueFormat2,
      pairSets: this.parseList(Parser.pointer(Parser.list(function() {
        return {
          secondGlyph: this.parseUShort(),
          value1: this.parseValueRecord(valueFormat1),
          value2: this.parseValueRecord(valueFormat2)
        };
      })))
    };
  } else if (posFormat === 2) {
    var classDef1 = this.parsePointer(Parser.classDef);
    var classDef2 = this.parsePointer(Parser.classDef);
    var class1Count = this.parseUShort();
    var class2Count = this.parseUShort();
    return {
      posFormat,
      coverage,
      valueFormat1,
      valueFormat2,
      classDef1,
      classDef2,
      class1Count,
      class2Count,
      classRecords: this.parseList(class1Count, Parser.list(class2Count, function() {
        return {
          value1: this.parseValueRecord(valueFormat1),
          value2: this.parseValueRecord(valueFormat2)
        };
      }))
    };
  }
};
subtableParsers$1[3] = function parseLookup32() {
  return { error: "GPOS Lookup 3 not supported" };
};
subtableParsers$1[4] = function parseLookup42() {
  return { error: "GPOS Lookup 4 not supported" };
};
subtableParsers$1[5] = function parseLookup52() {
  return { error: "GPOS Lookup 5 not supported" };
};
subtableParsers$1[6] = function parseLookup62() {
  return { error: "GPOS Lookup 6 not supported" };
};
subtableParsers$1[7] = function parseLookup72() {
  return { error: "GPOS Lookup 7 not supported" };
};
subtableParsers$1[8] = function parseLookup82() {
  return { error: "GPOS Lookup 8 not supported" };
};
subtableParsers$1[9] = function parseLookup9() {
  return { error: "GPOS Lookup 9 not supported" };
};
var subtableMakers$1 = new Array(10);
var PRECISION = 0.00000001;
var samePoint = (x, y) => samePoint$2(x, y, PRECISION * 10);
var getIntersectionPoint = (line1Start, line1End, line2Start, line2End) => {
  const x = determinant2x2([
    [
      determinant2x2([
        [line1Start[0], line1Start[1]],
        [line1End[0], line1End[1]]
      ]),
      determinant2x2([
        [line1Start[0], 1],
        [line1End[0], 1]
      ])
    ],
    [
      determinant2x2([
        [line2Start[0], line2Start[1]],
        [line2End[0], line2End[1]]
      ]),
      determinant2x2([
        [line2Start[0], 1],
        [line2End[0], 1]
      ])
    ]
  ]) / determinant2x2([
    [
      determinant2x2([
        [line1Start[0], 1],
        [line1End[0], 1]
      ]),
      determinant2x2([
        [line1Start[1], 1],
        [line1End[1], 1]
      ])
    ],
    [
      determinant2x2([
        [line2Start[0], 1],
        [line2End[0], 1]
      ]),
      determinant2x2([
        [line2Start[1], 1],
        [line2End[1], 1]
      ])
    ]
  ]);
  const y = determinant2x2([
    [
      determinant2x2([
        [line1Start[0], line1Start[1]],
        [line1End[0], line1End[1]]
      ]),
      determinant2x2([
        [line1Start[1], 1],
        [line1End[1], 1]
      ])
    ],
    [
      determinant2x2([
        [line2Start[0], line2Start[1]],
        [line2End[0], line2End[1]]
      ]),
      determinant2x2([
        [line2Start[1], 1],
        [line2End[1], 1]
      ])
    ]
  ]) / determinant2x2([
    [
      determinant2x2([
        [line1Start[0], 1],
        [line1End[0], 1]
      ]),
      determinant2x2([
        [line1Start[1], 1],
        [line1End[1], 1]
      ])
    ],
    [
      determinant2x2([
        [line2Start[0], 1],
        [line2End[0], 1]
      ]),
      determinant2x2([
        [line2Start[1], 1],
        [line2End[1], 1]
      ])
    ]
  ]);
  return [x, y];
};
function joinRound(appendCurve, previousLastPoint, firstPoint, previousCurve, _curve) {
  const arcJoiner = make2dArcFromCenter(previousLastPoint, firstPoint, previousCurve.original.lastPoint);
  appendCurve(previousCurve);
  appendCurve(arcJoiner);
}
function joinBevel(appendCurve, previousLastPoint, firstPoint, previousCurve, _curve) {
  const bevelJoiner = make2dSegmentCurve(previousLastPoint, firstPoint);
  appendCurve(previousCurve);
  appendCurve(bevelJoiner);
}
function joinMiter(appendCurve, previousLastPoint, firstPoint, previousCurve, curve) {
  const previousOtherPoint = previousCurve.offset instanceof Curve2D ? subtract2d(previousLastPoint, previousCurve.offset.tangentAt(1)) : previousCurve.offset.firstPoint;
  const nextOtherPoint = curve.offset instanceof Curve2D ? add2d(firstPoint, curve.offset.tangentAt(0)) : curve.offset.lastPoint;
  const offsetIntersectionPoint = getIntersectionPoint(previousOtherPoint, previousLastPoint, firstPoint, nextOtherPoint);
  const miterJoiner1 = make2dSegmentCurve(previousLastPoint, offsetIntersectionPoint);
  const miterJoiner2 = make2dSegmentCurve(offsetIntersectionPoint, firstPoint);
  appendCurve(previousCurve);
  appendCurve(miterJoiner1);
  appendCurve(miterJoiner2);
}
var OFFSET_JOINERS = {
  round: joinRound,
  bevel: joinBevel,
  miter: joinMiter
};
function rawOffsets(blueprint, offset2, offsetConfig = {}) {
  const correctedOffset = blueprint.orientation === "clockwise" ? -offset2 : offset2;
  const offsetCurves = blueprint.curves.map((c) => ({
    offset: make2dOffset(c, correctedOffset),
    original: c
  }));
  const offsettedArray = [];
  let savedLastCurve = null;
  let previousCurve = offsetCurves.at(-1);
  if (!previousCurve)
    return [];
  if (offsettedArray.length === 1)
    return offsettedArray;
  function appendCurve(curve) {
    if (curve instanceof Curve2D) {
      offsettedArray.push(curve);
      return;
    }
    if (!savedLastCurve) {
      savedLastCurve = curve;
    } else if (curve.offset instanceof Curve2D) {
      offsettedArray.push(curve.offset);
    } else if (!samePoint(curve.offset.firstPoint, curve.offset.lastPoint)) {
      offsettedArray.push(make2dSegmentCurve(curve.offset.firstPoint, curve.offset.lastPoint));
    }
  }
  const iterateOffsetCurves = function* () {
    for (const curve of offsetCurves.slice(0, -1)) {
      yield curve;
    }
    if (!savedLastCurve)
      throw new Error("Bug in the offset algorithm");
    yield savedLastCurve;
  };
  for (const curve of iterateOffsetCurves()) {
    const previousLastPoint = previousCurve.offset.lastPoint;
    const firstPoint = curve.offset.firstPoint;
    if (samePoint(previousLastPoint, firstPoint)) {
      appendCurve(previousCurve);
      previousCurve = curve;
      continue;
    }
    let intersections = [];
    if (previousCurve.offset instanceof Curve2D && curve.offset instanceof Curve2D) {
      const { intersections: pointIntersections, commonSegmentsPoints } = intersectCurves(previousCurve.offset, curve.offset, PRECISION / 100);
      intersections = [...pointIntersections, ...commonSegmentsPoints];
    }
    if (intersections.length > 0) {
      let intersection = intersections[0];
      if (intersections.length > 1) {
        const originalEndpoint = previousCurve == null ? undefined : previousCurve.original.lastPoint;
        const distances = intersections.map((i) => squareDistance2d(i, originalEndpoint));
        intersection = intersections[distances.indexOf(Math.min(...distances))];
      }
      const splitPreviousCurve = previousCurve.offset.splitAt([intersection], PRECISION)[0];
      const splitCurve = curve.offset.splitAt([intersection], PRECISION).at(-1);
      if (!splitCurve)
        throw new Error("Bug in the splitting algo in offset");
      appendCurve({
        offset: splitPreviousCurve,
        original: previousCurve.original
      });
      previousCurve = { offset: splitCurve, original: curve.original };
      continue;
    }
    const joiner = OFFSET_JOINERS[offsetConfig.lineJoinType ?? "round"];
    joiner(appendCurve, previousLastPoint, firstPoint, previousCurve, curve);
    previousCurve = curve;
  }
  appendCurve(previousCurve);
  return offsettedArray;
}
function offsetBlueprint(blueprint, offset2, offsetConfig = {}) {
  const offsettedArray = rawOffsets(blueprint, offset2, offsetConfig);
  if (offsettedArray.length < 2)
    return null;
  const allIntersections = /* @__PURE__ */ new Map;
  const updateIntersections = (index, newPoints) => {
    const intersections = allIntersections.get(index) || [];
    allIntersections.set(index, [...intersections, ...newPoints]);
  };
  offsettedArray.forEach((firstCurve, firstIndex) => {
    offsettedArray.slice(firstIndex + 1).forEach((secondCurve, secondIndex) => {
      const { intersections: rawIntersections, commonSegmentsPoints } = intersectCurves(firstCurve, secondCurve, PRECISION);
      const intersections = [
        ...rawIntersections,
        ...commonSegmentsPoints
      ].filter((intersection) => {
        const onFirstCurveExtremity = samePoint(intersection, firstCurve.firstPoint) || samePoint(intersection, firstCurve.lastPoint);
        const onSecondCurveExtremity = samePoint(intersection, secondCurve.firstPoint) || samePoint(intersection, secondCurve.lastPoint);
        return !(onFirstCurveExtremity && onSecondCurveExtremity);
      });
      if (!intersections.length)
        return;
      updateIntersections(firstIndex, intersections);
      updateIntersections(secondIndex + firstIndex + 1, intersections);
    });
  });
  if (!allIntersections.size) {
    const offsettedBlueprint = new Blueprint(offsettedArray);
    if (!blueprint.intersects(offsettedBlueprint))
      return offsettedBlueprint;
    return null;
  }
  const splitCurves = offsettedArray.flatMap((curve, index) => {
    if (!allIntersections.has(index))
      return curve;
    const intersections = allIntersections.get(index) || [];
    const splitCurves2 = curve.splitAt(intersections, PRECISION * 100);
    return splitCurves2;
  });
  const prunedCurves = splitCurves.filter((curve) => {
    const closeCurve = blueprint.curves.find((c) => c.distanceFrom(curve) < Math.abs(offset2) - PRECISION);
    return !closeCurve;
  });
  if (!prunedCurves.length)
    return null;
  const curvesGrouped = stitchCurves(prunedCurves);
  const blueprints = curvesGrouped.filter((c) => c.length > 1).map((c) => new Blueprint(c)).filter((b) => b.isClosed());
  if (!blueprints.length)
    return null;
  if (blueprints.length === 1) {
    return blueprints[0];
  }
  return new Blueprints(blueprints);
}
var fuseAll = (blueprints) => {
  let fused = blueprints[0];
  for (let i = 1;i < blueprints.length; i++) {
    fused = fuse2D(fused, blueprints[i]);
  }
  return fused;
};
function offset(bp, offsetDistance, offsetConfig = {}) {
  if (bp instanceof Blueprint) {
    return offsetBlueprint(bp, offsetDistance, offsetConfig);
  } else if (bp instanceof Blueprints) {
    return fuseAll(bp.blueprints.map((b) => offset(b, offsetDistance, offsetConfig)));
  } else if (bp instanceof CompoundBlueprint) {
    const innerShape = fuseAll(bp.blueprints.slice(1).map((b) => offset(b, offsetDistance, offsetConfig)));
    return cut2D(offset(bp.blueprints[0], offsetDistance, offsetConfig), innerShape);
  }
  return null;
}
function modifyCorners(makeCorner, blueprint, radius, finder) {
  let modifyCorner = () => true;
  if (finder) {
    modifyCorner = finder.shouldKeep.bind(finder);
  }
  const curves = [blueprint.curves[0]];
  const addModifiedCorner = (firstCurve, secondCurve) => {
    if (modifyCorner({ firstCurve, secondCurve, point: firstCurve.lastPoint })) {
      curves.push(...makeCorner(firstCurve, secondCurve, radius));
    } else {
      curves.push(firstCurve, secondCurve);
    }
  };
  blueprint.curves.slice(1).forEach((secondCurve) => {
    const firstCurve = curves.pop();
    if (!firstCurve)
      throw new Error("Bug in the blueprint filletting algo");
    addModifiedCorner(firstCurve, secondCurve);
  });
  const lastCurve = curves.at(-1);
  if (!lastCurve)
    throw new Error("Bug in the blueprint corner algo");
  if (samePoint$2(curves[0].firstPoint, lastCurve.lastPoint) && curves.length > 1) {
    const firstCurve = curves.pop();
    const secondCurve = curves.shift();
    if (!firstCurve || !secondCurve)
      throw new Error("Bug in the blueprint filletting algo");
    addModifiedCorner(firstCurve, secondCurve);
  }
  return new Blueprint(curves);
}
function modifyCorner2D(makeCorner, shape, radius, finder) {
  if (shape instanceof Blueprint) {
    return modifyCorners(makeCorner, shape, radius, finder);
  }
  if (shape instanceof CompoundBlueprint) {
    return new CompoundBlueprint(shape.blueprints.map((b) => modifyCorners(makeCorner, b, radius, finder)));
  }
  if (shape instanceof Blueprints) {
    const bps = shape.blueprints.map((b) => modifyCorner2D(makeCorner, b, radius, finder)).filter((b) => b !== null);
    return new Blueprints(bps);
  }
  return null;
}
function fillet2D(shape, radius, finder) {
  return modifyCorner2D(filletCurves, shape, radius, finder);
}
function chamfer2D(shape, radius, finder) {
  return modifyCorner2D(chamferCurves, shape, radius, finder);
}
function approximateForSVG(bp, options) {
  if (bp instanceof Blueprint) {
    return new Blueprint(approximateAsSvgCompatibleCurve(bp.curves, options));
  } else if (bp instanceof CompoundBlueprint) {
    return new CompoundBlueprint(bp.blueprints.map((b) => approximateForSVG(b, options)));
  } else if (bp instanceof Blueprints) {
    return new Blueprints(bp.blueprints.map((b) => approximateForSVG(b, options)));
  }
  return bp;
}

class Drawing {
  constructor(innerShape = null) {
    this.innerShape = innerShape;
  }
  clone() {
    var _a;
    return new Drawing(((_a = this.innerShape) == null ? undefined : _a.clone()) || null);
  }
  serialize() {
    function serializeHelper(shape) {
      if (shape instanceof CompoundBlueprint) {
        return {
          type: "CompoundBlueprint",
          blueprints: shape.blueprints.map(serializeHelper)
        };
      } else if (shape instanceof Blueprints) {
        return {
          type: "Blueprints",
          blueprints: shape.blueprints.map(serializeHelper)
        };
      } else if (shape instanceof Blueprint) {
        return {
          type: "Blueprint",
          curves: shape.curves.map((c) => c.serialize())
        };
      } else {
        throw new Error("Unknown shape type for serialization");
      }
    }
    return JSON.stringify(serializeHelper(this.innerShape));
  }
  get boundingBox() {
    if (!this.innerShape)
      return new BoundingBox2d;
    return this.innerShape.boundingBox;
  }
  stretch(ratio, direction, origin) {
    if (!this.innerShape)
      return new Drawing;
    return new Drawing(this.innerShape.stretch(ratio, direction, origin));
  }
  get repr() {
    if (this.innerShape === null)
      return "=== empty shape";
    return this.innerShape.repr;
  }
  rotate(angle, center) {
    if (!this.innerShape)
      return new Drawing;
    return new Drawing(this.innerShape.rotate(angle, center));
  }
  translate(xDistOrPoint, yDist = 0) {
    if (!this.innerShape)
      return new Drawing;
    return new Drawing(this.innerShape.translate(xDistOrPoint, yDist));
  }
  scale(scaleFactor, center) {
    if (!this.innerShape)
      return new Drawing;
    return new Drawing(this.innerShape.scale(scaleFactor, center));
  }
  mirror(centerOrDirection, origin, mode) {
    if (!this.innerShape)
      return new Drawing;
    return new Drawing(this.innerShape.mirror(centerOrDirection, origin, mode));
  }
  cut(other) {
    return new Drawing(cut2D(this.innerShape, other.innerShape));
  }
  fuse(other) {
    return new Drawing(fuse2D(this.innerShape, other.innerShape));
  }
  intersect(other) {
    return new Drawing(intersect2D(this.innerShape, other.innerShape));
  }
  fillet(radius, filter) {
    const finder = filter && filter(new CornerFinder);
    return new Drawing(fillet2D(this.innerShape, radius, finder));
  }
  chamfer(radius, filter) {
    const finder = filter && filter(new CornerFinder);
    return new Drawing(chamfer2D(this.innerShape, radius, finder));
  }
  sketchOnPlane(inputPlane, origin) {
    if (!this.innerShape)
      throw new Error("Trying to sketch an empty drawing");
    return this.innerShape.sketchOnPlane(inputPlane, origin);
  }
  sketchOnFace(face, scaleMode) {
    if (!this.innerShape)
      throw new Error("Trying to sketch an empty drawing");
    return this.innerShape.sketchOnFace(face, scaleMode);
  }
  punchHole(shape, faceFinder, options = {}) {
    if (!this.innerShape)
      return shape;
    return this.innerShape.punchHole(shape, faceFinder, options);
  }
  toSVG(margin) {
    var _a;
    return ((_a = this.innerShape) == null ? undefined : _a.toSVG(margin)) || "";
  }
  toSVGViewBox(margin = 1) {
    var _a;
    return ((_a = this.innerShape) == null ? undefined : _a.toSVGViewBox(margin)) || "";
  }
  toSVGPaths() {
    var _a;
    return ((_a = this.innerShape) == null ? undefined : _a.toSVGPaths()) || [];
  }
  offset(distance, offsetConfig = {}) {
    return new Drawing(offset(this.innerShape, distance, offsetConfig));
  }
  approximate(target, options = {}) {
    if (target !== "svg") {
      throw new Error("Only 'svg' is supported for now");
    }
    return new Drawing(approximateForSVG(this.innerShape, options));
  }
  get blueprint() {
    if (!(this.innerShape instanceof Blueprint)) {
      if (this.innerShape instanceof Blueprints && this.innerShape.blueprints.length === 1 && this.innerShape.blueprints[0] instanceof Blueprint) {
        return this.innerShape.blueprints[0];
      }
      throw new Error("This drawing is not a blueprint");
    }
    return this.innerShape;
  }
}

class DrawingPen extends BaseSketcher2d {
  constructor(origin = [0, 0]) {
    super();
    this.pointer = origin;
    this.firstPoint = origin;
    this.pendingCurves = [];
  }
  done() {
    return new Drawing(new Blueprint(this.pendingCurves));
  }
  close() {
    this._closeSketch();
    return this.done();
  }
  closeWithMirror() {
    this._closeWithMirror();
    return this.close();
  }
  closeWithCustomCorner(radius, mode = "fillet") {
    this._closeSketch();
    this._customCornerLastWithFirst(radius, mode);
    return this.done();
  }
}
function draw(initialPoint) {
  const pen = new DrawingPen;
  if (initialPoint) {
    pen.movePointerTo(initialPoint);
  }
  return pen;
}
function drawRoundedRectangle(width, height, r = 0) {
  return new Drawing(roundedRectangleBlueprint(width, height, r));
}
var drawRectangle = drawRoundedRectangle;
function drawCircle(radius) {
  return draw().movePointerTo([-radius, 0]).sagittaArc(2 * radius, 0, radius).sagittaArc(-2 * radius, 0, radius).close();
}
function uuidv() {
  return ([1e7] + -1000 + -4000 + -8000 + -100000000000).replace(/[018]/g, (c) => (c ^ crypto.getRandomValues(new Uint8Array(1))[0] & 15 >> c / 4).toString(16));
}
var wrapString = (str) => {
  const oc = getOC();
  return new oc.TCollection_ExtendedString_2(str, true);
};
function parseSlice(hex, index) {
  return parseInt(hex.slice(index * 2, (index + 1) * 2), 16);
}
function colorFromHex(hex) {
  let color = hex;
  if (color.indexOf("#") === 0)
    color = color.slice(1);
  if (color.length === 3) {
    color = color.replace(/([0-9a-f])/gi, "$1$1");
  }
  return [parseSlice(color, 0), parseSlice(color, 1), parseSlice(color, 2)];
}
var wrapColor = (hex, alpha = 1) => {
  const oc = getOC();
  const [r, g, b] = colorFromHex(hex);
  return new oc.Quantity_ColorRGBA_5(r / 255, g / 255, b / 255, alpha);
};

class AssemblyExporter extends WrappingObj {
}
function createAssembly(shapes = []) {
  const oc = getOC();
  const doc = new oc.TDocStd_Document(wrapString("XmlOcaf"));
  oc.XCAFDoc_ShapeTool.SetAutoNaming(false);
  const mainLabel = doc.Main();
  const tool = oc.XCAFDoc_DocumentTool.ShapeTool(mainLabel).get();
  const ctool = oc.XCAFDoc_DocumentTool.ColorTool(mainLabel).get();
  for (const { shape, name, color, alpha } of shapes) {
    const shapeNode = tool.NewShape();
    tool.SetShape(shapeNode, shape.wrapped);
    oc.TDataStd_Name.Set_1(shapeNode, wrapString(name || uuidv()));
    ctool.SetColor_3(shapeNode, wrapColor(color || "#f00", alpha ?? 1), oc.XCAFDoc_ColorType.XCAFDoc_ColorSurf);
  }
  tool.UpdateAssemblies();
  return new AssemblyExporter(doc);
}
function exportSTEP(shapes = [], { unit, modelUnit } = {}) {
  const oc = getOC();
  const r = GCWithScope();
  const doc = createAssembly(shapes);
  if (unit || modelUnit) {
    r(new oc.STEPCAFControl_Writer_1);
    oc.Interface_Static.SetCVal("xstep.cascade.unit", (modelUnit || unit || "MM").toUpperCase());
    oc.Interface_Static.SetCVal("write.step.unit", (unit || modelUnit || "MM").toUpperCase());
  }
  const session = r(new oc.XSControl_WorkSession);
  const writer = r(new oc.STEPCAFControl_Writer_2(r(new oc.Handle_XSControl_WorkSession_2(session)), false));
  writer.SetColorMode(true);
  writer.SetLayerMode(true);
  writer.SetNameMode(true);
  oc.Interface_Static.SetIVal("write.surfacecurve.mode", true);
  oc.Interface_Static.SetIVal("write.precision.mode", 0);
  oc.Interface_Static.SetIVal("write.step.assembly", 2);
  oc.Interface_Static.SetIVal("write.step.schema", 5);
  const progress = r(new oc.Message_ProgressRange_1);
  writer.Transfer_1(new oc.Handle_TDocStd_Document_2(doc.wrapped), oc.STEPControl_StepModelType.STEPControl_AsIs, null, progress);
  const filename = "export.step";
  const done = writer.Write(filename);
  if (done === oc.IFSelect_ReturnStatus.IFSelect_RetDone) {
    const file = oc.FS.readFile("/" + filename);
    oc.FS.unlink("/" + filename);
    const blob = new Blob([file], { type: "application/STEP" });
    return blob;
  } else {
    throw new Error("WRITE STEP FILE FAILED.");
  }
}

// src/runner.mjs
import { mkdir } from "fs/promises";
import { dirname, join } from "path";
var SCHEMA = "apteva-design/v1";
function fail2(message, details = undefined) {
  const err = new Error(message);
  if (details !== undefined)
    err.details = details;
  throw err;
}
function finite(value, label) {
  const number = Number(value);
  if (!Number.isFinite(number))
    fail2(`${label} must resolve to a finite number`);
  return number;
}

class ExpressionParser {
  constructor(source, params) {
    this.source = source;
    this.params = params;
    this.index = 0;
  }
  parse() {
    const result = this.expression();
    this.space();
    if (this.index !== this.source.length) {
      fail2(`unexpected token in expression ${JSON.stringify(this.source)} at ${this.index}`);
    }
    return finite(result, `expression ${JSON.stringify(this.source)}`);
  }
  expression() {
    let value = this.term();
    for (;; ) {
      this.space();
      if (this.take("+"))
        value += this.term();
      else if (this.take("-"))
        value -= this.term();
      else
        return value;
    }
  }
  term() {
    let value = this.unary();
    for (;; ) {
      this.space();
      if (this.take("*"))
        value *= this.unary();
      else if (this.take("/")) {
        const divisor = this.unary();
        if (divisor === 0)
          fail2(`division by zero in expression ${JSON.stringify(this.source)}`);
        value /= divisor;
      } else
        return value;
    }
  }
  unary() {
    this.space();
    if (this.take("+"))
      return this.unary();
    if (this.take("-"))
      return -this.unary();
    return this.primary();
  }
  primary() {
    this.space();
    if (this.take("(")) {
      const value = this.expression();
      this.space();
      if (!this.take(")"))
        fail2(`missing ')' in expression ${JSON.stringify(this.source)}`);
      return value;
    }
    const number = this.match(/^(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?/);
    if (number)
      return Number(number);
    const identifier = this.match(/^[A-Za-z_][A-Za-z0-9_]*/);
    if (identifier) {
      if (!Object.hasOwn(this.params, identifier))
        fail2(`unknown parameter ${identifier}`);
      return finite(this.params[identifier], `parameter ${identifier}`);
    }
    fail2(`expected number, parameter, or '(' in expression ${JSON.stringify(this.source)} at ${this.index}`);
  }
  match(pattern) {
    const found = this.source.slice(this.index).match(pattern);
    if (!found)
      return "";
    this.index += found[0].length;
    return found[0];
  }
  take(value) {
    if (!this.source.startsWith(value, this.index))
      return false;
    this.index += value.length;
    return true;
  }
  space() {
    while (/\s/.test(this.source[this.index] || ""))
      this.index += 1;
  }
}
function resolve(value, params, label = "value") {
  if (typeof value === "number")
    return finite(value, label);
  if (typeof value === "string")
    return new ExpressionParser(value, params).parse();
  fail2(`${label} must be a number or arithmetic parameter expression`);
}
function vector(value, params, label, fallback = [0, 0, 0]) {
  const source = value ?? fallback;
  if (!Array.isArray(source) || source.length !== 3)
    fail2(`${label} must contain three values`);
  return source.map((part, index) => resolve(part, params, `${label}[${index}]`));
}
function parameterValues(definition, supplied) {
  const output = {};
  for (const [name, spec] of Object.entries(definition.parameters || {})) {
    const candidate = Object.hasOwn(supplied || {}, name) ? supplied[name] : spec.default;
    const value = finite(candidate, `parameter ${name}`);
    if (spec.min !== undefined && value < finite(spec.min, `${name}.min`)) {
      fail2(`parameter ${name} is below its minimum`);
    }
    if (spec.max !== undefined && value > finite(spec.max, `${name}.max`)) {
      fail2(`parameter ${name} is above its maximum`);
    }
    output[name] = value;
  }
  for (const name of Object.keys(supplied || {})) {
    if (!Object.hasOwn(output, name))
      fail2(`unknown parameter ${name}`);
  }
  return output;
}
function shapeFor(id, shapes) {
  const shape = shapes.get(id);
  if (!shape)
    fail2(`operation references unknown shape ${id}`);
  return shape;
}
function polygonSketch(points, params, label, plane = "XY", origin = [0, 0, 0]) {
  if (!Array.isArray(points) || points.length < 3)
    fail2(`${label} needs at least three [x,y] points`);
  const resolved = points.map((point, index) => {
    if (!Array.isArray(point) || point.length !== 2)
      fail2(`${label}[${index}] must be [x,y]`);
    return [resolve(point[0], params), resolve(point[1], params)];
  });
  let pen = draw(resolved[0]);
  for (const point of resolved.slice(1))
    pen = pen.lineTo(point);
  return pen.close().sketchOnPlane(plane, vector(origin, params, `${label}.origin`));
}
function buildOperation(operation, shapes, params) {
  if (!operation || typeof operation !== "object")
    fail2("every operation must be an object");
  if (!operation.id || typeof operation.id !== "string")
    fail2("every operation requires a string id");
  if (shapes.has(operation.id))
    fail2(`duplicate operation id ${operation.id}`);
  let shape;
  switch (operation.type) {
    case "box": {
      const [width, depth, height] = vector(operation.size, params, `${operation.id}.size`);
      if (width <= 0 || depth <= 0 || height <= 0)
        fail2(`${operation.id}.size must be positive`);
      const [x, y, z] = vector(operation.origin, params, `${operation.id}.origin`);
      shape = makeBox([x, y, z], [x + width, y + depth, z + height]);
      break;
    }
    case "cylinder": {
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      const height = resolve(operation.height, params, `${operation.id}.height`);
      if (radius <= 0 || height <= 0)
        fail2(`${operation.id} radius and height must be positive`);
      shape = makeCylinder(radius, height, vector(operation.origin, params, `${operation.id}.origin`), vector(operation.direction, params, `${operation.id}.direction`, [0, 0, 1]));
      break;
    }
    case "sphere": {
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      if (radius <= 0)
        fail2(`${operation.id}.radius must be positive`);
      shape = makeSphere(radius).translate(vector(operation.center, params, `${operation.id}.center`));
      break;
    }
    case "extrude_rectangle": {
      const width = resolve(operation.width, params, `${operation.id}.width`);
      const depth = resolve(operation.depth, params, `${operation.id}.depth`);
      const height = resolve(operation.height, params, `${operation.id}.height`);
      if (width <= 0 || depth <= 0 || height <= 0)
        fail2(`${operation.id} dimensions must be positive`);
      const [x, y, z] = vector(operation.origin, params, `${operation.id}.origin`);
      shape = drawRectangle(width, depth).translate(x + width / 2, y + depth / 2).sketchOnPlane("XY", z).extrude(height);
      break;
    }
    case "extrude_circle": {
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      const height = resolve(operation.height, params, `${operation.id}.height`);
      if (radius <= 0 || height <= 0)
        fail2(`${operation.id} radius and height must be positive`);
      const [x, y, z] = vector(operation.origin, params, `${operation.id}.origin`);
      shape = drawCircle(radius).translate(x, y).sketchOnPlane("XY", z).extrude(height);
      break;
    }
    case "extrude_polygon": {
      if (!Array.isArray(operation.points) || operation.points.length < 3) {
        fail2(`${operation.id}.points needs at least three [x,y] points`);
      }
      const points = operation.points.map((point, index) => {
        if (!Array.isArray(point) || point.length !== 2)
          fail2(`${operation.id}.points[${index}] must be [x,y]`);
        return [resolve(point[0], params), resolve(point[1], params)];
      });
      let pen = draw(points[0]);
      for (const point of points.slice(1))
        pen = pen.lineTo(point);
      const height = resolve(operation.height, params, `${operation.id}.height`);
      const z = resolve(operation.z ?? 0, params, `${operation.id}.z`);
      shape = pen.close().sketchOnPlane("XY", z).extrude(height);
      break;
    }
    case "revolve_profile": {
      const sketch = polygonSketch(operation.points, params, `${operation.id}.points`, operation.plane || "XZ", operation.origin);
      shape = sketch.revolve(vector(operation.axis, params, `${operation.id}.axis`, [0, 0, 1]), { origin: vector(operation.axis_origin, params, `${operation.id}.axis_origin`), angle: resolve(operation.angle ?? 360, params, `${operation.id}.angle`) });
      break;
    }
    case "sweep_circle": {
      if (!Array.isArray(operation.path) || operation.path.length < 2)
        fail2(`${operation.id}.path needs at least two [x,y] points`);
      const path = operation.path.map((point, index) => {
        if (!Array.isArray(point) || point.length !== 2)
          fail2(`${operation.id}.path[${index}] must be [x,y]`);
        return [resolve(point[0], params), resolve(point[1], params)];
      });
      let pen = draw(path[0]);
      for (const point of path.slice(1))
        pen = pen.lineTo(point);
      const spine = pen.done().sketchOnPlane(operation.plane || "XY", vector(operation.origin, params, `${operation.id}.origin`));
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      if (radius <= 0)
        fail2(`${operation.id}.radius must be positive`);
      shape = spine.sweepSketch((profilePlane, profileOrigin) => drawCircle(radius).sketchOnPlane(profilePlane, profileOrigin));
      break;
    }
    case "fuse":
    case "cut":
    case "intersect": {
      const ids = operation.inputs || [operation.base, ...operation.tools || []];
      if (!Array.isArray(ids) || ids.length < 2 || ids.some((id) => typeof id !== "string")) {
        fail2(`${operation.id}.${operation.type} needs at least two input ids`);
      }
      shape = shapeFor(ids[0], shapes).clone();
      for (const id of ids.slice(1)) {
        const tool = shapeFor(id, shapes).clone();
        shape = operation.type === "fuse" ? shape.fuse(tool) : operation.type === "cut" ? shape.cut(tool) : shape.intersect(tool);
      }
      break;
    }
    case "compound": {
      const ids = operation.inputs;
      if (!Array.isArray(ids) || ids.length === 0)
        fail2(`${operation.id}.inputs is required`);
      shape = makeCompound(ids.map((id) => shapeFor(id, shapes).clone()));
      break;
    }
    case "translate": {
      shape = shapeFor(operation.input, shapes).clone().translate(vector(operation.vector, params, `${operation.id}.vector`));
      break;
    }
    case "rotate": {
      shape = shapeFor(operation.input, shapes).clone().rotate(resolve(operation.angle, params, `${operation.id}.angle`), vector(operation.center, params, `${operation.id}.center`), vector(operation.direction, params, `${operation.id}.direction`, [0, 0, 1]));
      break;
    }
    case "scale": {
      shape = shapeFor(operation.input, shapes).clone().scale(resolve(operation.factor, params, `${operation.id}.factor`), vector(operation.center, params, `${operation.id}.center`));
      break;
    }
    case "mirror": {
      shape = shapeFor(operation.input, shapes).clone().mirror(operation.plane || "YZ", vector(operation.origin, params, `${operation.id}.origin`));
      break;
    }
    case "linear_pattern": {
      const count = Math.trunc(resolve(operation.count, params, `${operation.id}.count`));
      if (count < 1 || count > 128)
        fail2(`${operation.id}.count must be between 1 and 128`);
      const step = vector(operation.step, params, `${operation.id}.step`);
      const copies = [];
      for (let index = 0;index < count; index += 1) {
        copies.push(shapeFor(operation.input, shapes).clone().translate(step.map((value) => value * index)));
      }
      shape = copies[0];
      for (const copy of copies.slice(1))
        shape = operation.fuse ? shape.fuse(copy) : null;
      if (!operation.fuse)
        shape = makeCompound(copies);
      break;
    }
    case "circular_pattern": {
      const count = Math.trunc(resolve(operation.count, params, `${operation.id}.count`));
      if (count < 1 || count > 128)
        fail2(`${operation.id}.count must be between 1 and 128`);
      const totalAngle = resolve(operation.angle ?? 360, params, `${operation.id}.angle`);
      const center = vector(operation.center, params, `${operation.id}.center`);
      const direction = vector(operation.direction, params, `${operation.id}.direction`, [0, 0, 1]);
      const copies = [];
      for (let index = 0;index < count; index += 1) {
        copies.push(shapeFor(operation.input, shapes).clone().rotate(totalAngle * index / count, center, direction));
      }
      shape = copies[0];
      for (const copy of copies.slice(1))
        shape = operation.fuse ? shape.fuse(copy) : null;
      if (!operation.fuse)
        shape = makeCompound(copies);
      break;
    }
    case "fillet": {
      const radius = resolve(operation.radius, params, `${operation.id}.radius`);
      if (radius <= 0)
        fail2(`${operation.id}.radius must be positive`);
      shape = shapeFor(operation.input, shapes).clone().fillet(radius);
      break;
    }
    case "chamfer": {
      const distance = resolve(operation.distance, params, `${operation.id}.distance`);
      if (distance <= 0)
        fail2(`${operation.id}.distance must be positive`);
      shape = shapeFor(operation.input, shapes).clone().chamfer(distance);
      break;
    }
    default:
      fail2(`unsupported operation type ${JSON.stringify(operation.type)}`);
  }
  shapes.set(operation.id, shape);
}
function applyAssemblyTransform(shape, transform, params) {
  let output = shape.clone();
  if (transform?.scale !== undefined) {
    output = output.scale(resolve(transform.scale, params, "assembly scale"), [0, 0, 0]);
  }
  for (const rotation of transform?.rotate || []) {
    output = output.rotate(resolve(rotation.angle, params, "assembly rotation angle"), vector(rotation.center, params, "assembly rotation center"), vector(rotation.direction, params, "assembly rotation direction", [0, 0, 1]));
  }
  if (transform?.translate)
    output = output.translate(vector(transform.translate, params, "assembly translation"));
  return output;
}
function rotatePoint(point, angleDegrees, center, axis) {
  const radians = angleDegrees * Math.PI / 180;
  const length = Math.hypot(...axis);
  if (length === 0)
    fail2("assembly rotation direction must not be zero");
  const [ux, uy, uz] = axis.map((value) => value / length);
  const [x, y, z] = point.map((value, index) => value - center[index]);
  const cosine = Math.cos(radians), sine = Math.sin(radians), dot = ux * x + uy * y + uz * z;
  return [
    center[0] + x * cosine + (uy * z - uz * y) * sine + ux * dot * (1 - cosine),
    center[1] + y * cosine + (uz * x - ux * z) * sine + uy * dot * (1 - cosine),
    center[2] + z * cosine + (ux * y - uy * x) * sine + uz * dot * (1 - cosine)
  ];
}
function transformPhysical(physical, transform, params) {
  if (!physical)
    return null;
  let center = [...physical.center];
  let mass = physical.mass;
  if (transform?.scale !== undefined) {
    const factor = resolve(transform.scale, params, "assembly scale");
    center = center.map((value) => value * factor);
    mass *= Math.abs(factor ** 3);
  }
  for (const rotation of transform?.rotate || []) {
    center = rotatePoint(center, resolve(rotation.angle, params, "assembly rotation angle"), vector(rotation.center, params, "assembly rotation center"), vector(rotation.direction, params, "assembly rotation direction", [0, 0, 1]));
  }
  if (transform?.translate) {
    const translation = vector(transform.translate, params, "assembly translation");
    center = center.map((value, index) => value + translation[index]);
  }
  return { mass, center };
}
function hexColor(value, fallback = "#64748b") {
  return typeof value === "string" && /^#[0-9a-f]{6}$/i.test(value) ? value : fallback;
}
function assemblyGeometry(definition, shapes, partShapes, partPhysical, partVisuals, params) {
  const parts = new Map((definition.parts || []).map((part) => [part.id, part]));
  const materials = new Map((definition.materials || []).map((material) => [material.id, material]));
  if (!definition.assembly?.instances?.length) {
    const shape = shapeFor(definition.output, shapes);
    const outputPart = (definition.parts || []).find((part) => part.output === definition.output);
    const visuals = outputPart ? partVisuals.get(outputPart.id) : [{ id: definition.output, part_id: definition.output, name: definition.output, color: "#64748b", shape }];
    return { shape, instances: [], visuals, parts, materials };
  }
  const instances = [];
  for (const instance of definition.assembly.instances) {
    if (instance.visible === false)
      continue;
    const part = parts.get(instance.part_id);
    if (!part)
      fail2(`assembly instance ${instance.id} references unknown part ${instance.part_id}`);
    const sourceShape = partShapes.get(part.id);
    if (!sourceShape)
      fail2(`assembly part ${part.id} has no resolved geometry`);
    const shape = applyAssemblyTransform(sourceShape, instance.transform, params);
    const material = materials.get(part.material_id);
    const visuals = (partVisuals.get(part.id) || [{ id: part.id, part_id: part.id, name: part.name, color: hexColor(part.color || material?.color), shape: sourceShape }]).map((visual) => ({
      ...visual,
      id: visual.id === part.id ? instance.id : `${instance.id}/${visual.id}`,
      shape: visual.shape === sourceShape ? shape : applyAssemblyTransform(visual.shape, instance.transform, params)
    }));
    instances.push({
      id: instance.id,
      part,
      material,
      shape,
      color: hexColor(part.color || material?.color),
      physical: transformPhysical(partPhysical.get(part.id), instance.transform, params),
      visuals
    });
  }
  if (!instances.length)
    fail2("assembly has no visible instances");
  return { shape: makeCompound(instances.map((item) => item.shape.clone())), instances, visuals: instances.flatMap((item) => item.visuals), parts, materials };
}
function validateDefinition(definition) {
  if (!definition || typeof definition !== "object")
    fail2("definition must be an object");
  if (definition.schema !== SCHEMA)
    fail2(`schema must be ${SCHEMA}`);
  if ((definition.units || "mm") !== "mm")
    fail2("v0.1 supports millimetres only");
  if (!Array.isArray(definition.operations))
    fail2("definition.operations must be an array");
  if (definition.operations.length === 0 && !definition.assembly?.instances?.length) {
    fail2("definition requires operations or an assembly with instances");
  }
  if ((!definition.output || typeof definition.output !== "string") && !definition.assembly)
    fail2("definition.output is required outside an assembly");
  if (definition.operations.length > 256)
    fail2("definition exceeds the 256-operation limit");
}
function buildDefinitionGeometry(definition, supplied) {
  validateDefinition(definition);
  const params = parameterValues(definition, supplied || {});
  const shapes = new Map;
  for (const operation of definition.operations)
    buildOperation(operation, shapes, params);
  const resolved = new Map((definition._resolved_components || []).map((component) => [component.part_id, component]));
  const partShapes = new Map;
  const partPhysical = new Map;
  const partVisuals = new Map;
  const materials = new Map((definition.materials || []).map((material) => [material.id, material]));
  for (const part of definition.parts || []) {
    if (part.source) {
      const component = resolved.get(part.id);
      if (!component)
        fail2(`linked part ${part.id} was not resolved by Design Studio`);
      const child = buildDefinitionGeometry(component.definition, component.parameters || {});
      const selected = component.source_part_id ? child.partShapes.get(component.source_part_id) : child.assembly.shape;
      if (!selected)
        fail2(`linked part ${part.id} source part ${component.source_part_id} has no geometry`);
      partShapes.set(part.id, selected.clone());
      const physical2 = component.source_part_id ? child.partPhysical.get(component.source_part_id) : child.physical;
      if (physical2)
        partPhysical.set(part.id, physical2);
      const visuals = component.source_part_id ? child.partVisuals.get(component.source_part_id) : child.assembly.visuals;
      if (visuals?.length)
        partVisuals.set(part.id, visuals);
    } else {
      const partShape = shapeFor(part.output, shapes);
      partShapes.set(part.id, partShape);
      const density = Number(materials.get(part.material_id)?.density_g_cm3 || 0);
      partPhysical.set(part.id, { mass: density > 0 ? measureVolume(partShape) * density / 1000 : 0, center: exactCenterOfMass(partShape) });
      partVisuals.set(part.id, [{ id: part.id, part_id: part.id, name: part.name, color: hexColor(part.color || materials.get(part.material_id)?.color), shape: partShape }]);
    }
  }
  const assembly = assemblyGeometry(definition, shapes, partShapes, partPhysical, partVisuals, params);
  let physical;
  if (assembly.instances.length) {
    const mass = assembly.instances.reduce((sum, item) => sum + (item.physical?.mass || 0), 0);
    const center = mass > 0 ? [0, 1, 2].map((axis) => assembly.instances.reduce((sum, item) => sum + (item.physical?.center[axis] || 0) * (item.physical?.mass || 0), 0) / mass) : exactCenterOfMass(assembly.shape);
    physical = { mass, center };
  } else {
    const matching = (definition.parts || []).find((part) => part.output === definition.output);
    physical = matching ? partPhysical.get(matching.id) : { mass: 0, center: exactCenterOfMass(assembly.shape) };
  }
  return { params, shapes, partShapes, partPhysical, partVisuals, assembly, physical };
}
async function writeBlob(path, blob) {
  await mkdir(dirname(path), { recursive: true });
  await Bun.write(path, new Uint8Array(await blob.arrayBuffer()));
}
function shapeBounds(shape) {
  const box = shape.boundingBox;
  const [min, max] = box.bounds;
  return { min, max, size: [box.width, box.height, box.depth], center: box.center };
}
function exactCenterOfMass(shape) {
  const properties = measureShapeVolumeProperties(shape);
  try {
    return properties.centerOfMass;
  } finally {
    properties.delete();
  }
}
function geometryReport(shape, mesh, instances = []) {
  const box = shape.boundingBox;
  const [min, max] = box.bounds;
  const volume = measureVolume(shape);
  const area = measureArea(shape);
  const partReports = instances.map((item) => {
    const partVolume = measureVolume(item.shape);
    const bounds = shapeBounds(item.shape);
    const center = item.physical?.center || exactCenterOfMass(item.shape);
    return {
      part_id: item.part.id,
      instance_id: item.id,
      name: item.part.name,
      material_id: item.part.material_id || "",
      volume_mm3: partVolume,
      mass_g: item.physical?.mass || 0,
      center_of_mass: center,
      bounds
    };
  });
  const mass = partReports.reduce((sum, item) => sum + item.mass_g, 0);
  const centerOfMass = mass > 0 ? [0, 1, 2].map((axis) => partReports.reduce((sum, item) => sum + item.center_of_mass[axis] * item.mass_g, 0) / mass) : exactCenterOfMass(shape);
  return {
    valid: Number.isFinite(volume) && volume > 0 && mesh.triangles.length >= 3,
    representation: "brep",
    units: "mm",
    bounds: { min, max, size: [box.width, box.height, box.depth], center: box.center },
    volume_mm3: volume,
    surface_area_mm2: area,
    body_count: instances.length || 1,
    face_count: shape.faces.length,
    edge_count: shape.edges.length,
    vertex_count: mesh.vertices.length / 3,
    triangle_count: mesh.triangles.length / 3,
    mass_g: mass,
    center_of_mass: centerOfMass,
    parts: partReports
  };
}
async function run(request) {
  const built = buildDefinitionGeometry(request.definition, request.parameters || {});
  const { params, partShapes, assembly } = built;
  const shape = assembly.shape;
  const tolerance = finite(request.tolerance ?? 0.1, "tolerance");
  const angularTolerance = finite(request.angular_tolerance ?? 0.1, "angular_tolerance");
  const mesh = shape.mesh({ tolerance, angularTolerance });
  const report = geometryReport(shape, mesh, assembly.instances);
  if (!report.valid)
    fail2("output geometry is empty or invalid", report);
  const outputDir = request.output_dir;
  if (!outputDir || typeof outputDir !== "string")
    fail2("output_dir is required");
  await mkdir(outputDir, { recursive: true });
  const artifacts = [];
  const formats = new Set(request.formats || ["mesh-json"]);
  if (formats.has("mesh-json")) {
    const path = join(outputDir, "model.mesh.json");
    const partMeshes = assembly.visuals.map((item) => ({
      id: item.id,
      part_id: item.part_id,
      name: item.name,
      color: item.color,
      ...item.shape.mesh({ tolerance, angularTolerance }),
      edges: item.shape.meshEdges({ tolerance, angularTolerance })
    }));
    await Bun.write(path, JSON.stringify({ ...mesh, edges: shape.meshEdges({ tolerance, angularTolerance }), parts: partMeshes }));
    artifacts.push({ format: "mesh-json", path, content_type: "application/json" });
    for (const part of request.definition.parts || []) {
      if (["purchased", "reference"].includes(part.manufacturing?.classification))
        continue;
      const partPath = join(outputDir, `${part.id}.mesh.json`);
      const partShape = partShapes.get(part.id);
      if (!partShape)
        fail2(`part ${part.id} has no export geometry`);
      const partMesh = partShape.mesh({ tolerance, angularTolerance });
      await Bun.write(partPath, JSON.stringify({ ...partMesh, edges: partShape.meshEdges({ tolerance, angularTolerance }) }));
      artifacts.push({ format: "mesh-json", path: partPath, content_type: "application/json", part_id: part.id, part_name: part.name });
    }
  }
  if (formats.has("step")) {
    const path = join(outputDir, "model.step");
    const stepShapes = assembly.instances.length ? assembly.instances.map((item) => ({ shape: item.shape, name: item.id })) : [{ shape, name: request.name || "Design" }];
    await writeBlob(path, exportSTEP(stepShapes, { unit: "MM", modelUnit: "MM" }));
    artifacts.push({ format: "step", path, content_type: "model/step" });
    for (const part of request.definition.parts || []) {
      if (["purchased", "reference"].includes(part.manufacturing?.classification))
        continue;
      const partPath = join(outputDir, `${part.id}.step`);
      const partShape = partShapes.get(part.id);
      if (!partShape)
        fail2(`part ${part.id} has no export geometry`);
      await writeBlob(partPath, exportSTEP([{ shape: partShape, name: part.name }], { unit: "MM", modelUnit: "MM" }));
      artifacts.push({ format: "step", path: partPath, content_type: "model/step", part_id: part.id, part_name: part.name });
    }
  }
  if (formats.has("stl")) {
    const path = join(outputDir, "model.stl");
    await writeBlob(path, shape.blobSTL({ tolerance, angularTolerance, binary: true }));
    artifacts.push({ format: "stl", path, content_type: "model/stl" });
    for (const part of request.definition.parts || []) {
      if (["purchased", "reference"].includes(part.manufacturing?.classification))
        continue;
      const partPath = join(outputDir, `${part.id}.stl`);
      const partShape = partShapes.get(part.id);
      if (!partShape)
        fail2(`part ${part.id} has no export geometry`);
      await writeBlob(partPath, partShape.blobSTL({ tolerance, angularTolerance, binary: true }));
      artifacts.push({ format: "stl", path: partPath, content_type: "model/stl", part_id: part.id, part_name: part.name });
    }
  }
  return { parameters: params, report, artifacts };
}
async function main() {
  const wasmPath = join(import.meta.dir, "replicad_single.wasm");
  const oc = await replicad_single_default({ locateFile: () => wasmPath });
  setOC(oc);
  const request = await Bun.stdin.json();
  return run(request);
}
try {
  const result = await main();
  process.stdout.write(`${JSON.stringify({ ok: true, ...result })}
`);
  process.exit(0);
} catch (error) {
  process.stdout.write(`${JSON.stringify({
    ok: false,
    error: error instanceof Error ? error.message : String(error),
    details: error?.details
  })}
`);
  process.exitCode = 1;
}
