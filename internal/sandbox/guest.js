// Called with a JSON-encoded source string by engine.go. Capture the private streams
// before entering user code. All policy and size checks also run on the Go side.
((source, mode, maxOutputBytes) => {
    const input = std.in, output = std.out;
    const write = output.puts.bind(output), flush = output.flush.bind(output);
    const read = input.getline.bind(input);
    const parse = JSON.parse, stringify = JSON.stringify;
    const AsyncFunction = (async function () {}).constructor;
    const NativePromise = Promise;
    const settle = Function.call.bind(Promise.prototype.then);
    for (const name of ["std", "os", "bjson", "scriptArgs", "print", "console", "loadScript"]) {
        delete globalThis[name];
    }
    function send(frame) {
        write(stringify(frame) + "\n");
        flush();
    }
    const hostErrors = new WeakMap(), argumentErrors = new WeakSet(), pageErrors = new WeakMap();
    function call(operation, args = {}, kind = "call") {
        if (typeof operation !== "string" || !operation || !args || typeof args !== "object" || Array.isArray(args)) {
            const error = new TypeError("api.call requires an operation identifier and an argument object.");
            argumentErrors.add(error);
            throw error;
        }
        send({kind, operation, args});
        const reply = parse(read());
        if (reply.error !== undefined) {
            const error = new Error(reply.error);
            hostErrors.set(error, reply.call);
            throw error;
        }
        return reply.value;
    }
    function fail(error) {
        if (hostErrors.has(error)) send({kind: "hostError", call: hostErrors.get(error)});
        else if (pageErrors.has(error)) send({kind: pageErrors.get(error)});
        else if (argumentErrors.has(error)) send({kind: "arguments"});
        else if (error instanceof SyntaxError) send({kind: "syntax"});
        else if (error instanceof ReferenceError) send({kind: "reference"});
        else if (error instanceof TypeError) send({kind: "type"});
        else if (error instanceof RangeError) send({kind: "range"});
        else if (String(error?.message) === "out of memory") send({kind: "memory"});
        else send({kind: "exception"});
    }
    function encode(value, checkpointNumbers = false) {
        const json = stringify(value, (key, item) => {
            if (item === undefined || typeof item === "function" || typeof item === "symbol" ||
                typeof item === "number" && !Number.isFinite(item) || item instanceof NativePromise) {
                throw new Error();
            }
            if (checkpointNumbers && Number.isInteger(item) && !Number.isSafeInteger(item)) {
                pageError("unsafe_integer", "Use strings to retain integers outside JavaScript's safe range.");
            }
            return item;
        });
        if (json === undefined) throw new Error();
        return json;
    }
    function finish(value) {
        try {
            const json = encode(value);
            if (json.length > maxOutputBytes) { send({kind: "output"}); return; }
            write('{"kind":"return","value":' + json + '}\n');
            flush();
        } catch (_) {
            send({kind: "result"});
        }
    }
    function remainingCalls() {
        send({kind: "remainingCalls"});
        return parse(read());
    }
    function pageError(kind, message) {
        const error = new Error(message);
        pageErrors.set(error, kind);
        throw error;
    }
    function validCursor(value) {
        return value === null || typeof value === "string" || Number.isSafeInteger(value);
    }
    // A returned checkpoint must fit even when one Unicode character occupies several bytes.
    // Parsing also releases references to the page and gives resumed and uninterrupted runs the same state.
    function checkpoint(state, complete, nextCursor) {
        let json;
        try {
            json = encode({state, complete, nextCursor}, true);
        } catch (error) {
            if (pageErrors.has(error)) throw error;
            pageError("result", "Return a JSON accumulator from reduce.");
        }
        if (json.length <= maxOutputBytes) {
            let bytes = 0;
            for (const character of json) {
                const point = character.codePointAt(0);
                bytes += point < 0x80 ? 1 : point < 0x800 ? 2 : point < 0x10000 ? 3 : 4;
            }
            if (bytes <= maxOutputBytes) return parse(json);
        }
        pageError("output", "Keep the pagination state and its cursor within the output limit.");
    }
    async function reducePages(operation, options) {
        if (!options || typeof options !== "object") pageError("pagination", "Supply pagination options.");
        const {cursorParam, next, reduce, cursor = null, initial, args = {}} = options;
        if (typeof operation !== "string" || !operation || typeof cursorParam !== "string" || !cursorParam ||
            typeof next !== "function" || typeof reduce !== "function" || !validCursor(cursor) ||
            !args || typeof args !== "object" || Array.isArray(args) ||
            (args.query !== undefined && (!args.query || typeof args.query !== "object" ||
                Array.isArray(args.query) || Object.hasOwn(args.query, cursorParam)))) {
            pageError("pagination", "Supply the GET, cursor parameter, fixed filters, next and reduce functions.");
        }
        const filters = parse(encode(args));
        const visited = new Set([cursor]);
        let result = checkpoint(initial, false, cursor);
        while (remainingCalls() > 0) {
            const current = result.nextCursor;
            const params = current === null ? filters :
                {...filters, query: {...filters.query, [cursorParam]: current}};
            const page = call(operation, params, "readPage");
            const following = await next(page, current);
            if (!validCursor(following) || (following !== null && visited.has(following))) {
                pageError("pagination", "Return null at the end or the next distinct string or integer cursor.");
            }
            result = checkpoint(await reduce(result.state, page), following === null, following);
            if (result.complete) break;
            visited.add(following);
        }
        return result;
    }
    function compile() {
        const body = '"use strict";\n' + source;
        if (mode === "async") return AsyncFunction("api", body);
        // Keep every body the old parser accepted on its original call API.
        // Only compilation can fall back; a running body may already have issued writes.
        try {
            return Function("api", body);
        } catch (error) {
            if (mode === "sync" || !(error instanceof SyntaxError)) throw error;
            return AsyncFunction("api", body);
        }
    }
    try {
        const data = read();
        const program = compile();
        const callAPI = program instanceof AsyncFunction ? async (op, args) => call(op, args) : (op, args) => call(op, args);
        const value = program(Object.freeze({
            call: callAPI,
            ...(data ? {input: parse(data)} : {}),
            reducePages,
            get remainingCalls() { return remainingCalls(); }
        }));
        if (value instanceof NativePromise) settle(value, finish, fail);
        else finish(value); // Seal ordinary results before QuickJS drains queued jobs.
    } catch (error) {
        fail(error);
    }
})
