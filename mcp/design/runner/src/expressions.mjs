// Safe parameter expressions shared by every Design Studio operation field.
// Kept free of OpenCascade so it can be unit-tested on its own.

function fail(message) {
  throw new Error(message);
}

function finite(value, label) {
  const number = Number(value);
  if (!Number.isFinite(number)) fail(`${label} must resolve to a finite number`);
  return number;
}

// Expressions: numbers, parameters, + - * / %, comparisons (< <= > >= == !=),
// and/or/not (&& || !), cond ? a : b, true/false/pi, and whitelisted
// functions. Booleans are 1/0. There is no property access, assignment,
// loop, or call to anything outside FUNCTIONS.
const DEG = Math.PI / 180;
const FUNCTIONS = {
  min: { min: 1, fn: (...a) => Math.min(...a) },
  max: { min: 1, fn: (...a) => Math.max(...a) },
  abs: { min: 1, max: 1, fn: Math.abs },
  floor: { min: 1, max: 1, fn: Math.floor },
  ceil: { min: 1, max: 1, fn: Math.ceil },
  round: { min: 1, max: 2, fn: (x, step) => step === undefined ? Math.round(x) : (step === 0 ? fail("round step must not be 0") : Math.round(x / step) * step) },
  sqrt: { min: 1, max: 1, fn: (x) => x < 0 ? fail("sqrt of a negative number") : Math.sqrt(x) },
  pow: { min: 2, max: 2, fn: Math.pow },
  clamp: { min: 3, max: 3, fn: (x, lo, hi) => Math.min(Math.max(x, lo), hi) },
  sin: { min: 1, max: 1, fn: (x) => Math.sin(x * DEG) },
  cos: { min: 1, max: 1, fn: (x) => Math.cos(x * DEG) },
  tan: { min: 1, max: 1, fn: (x) => Math.tan(x * DEG) },
  asin: { min: 1, max: 1, fn: (x) => Math.asin(x) / DEG },
  acos: { min: 1, max: 1, fn: (x) => Math.acos(x) / DEG },
  atan: { min: 1, max: 1, fn: (x) => Math.atan(x) / DEG },
  atan2: { min: 2, max: 2, fn: (y, x) => Math.atan2(y, x) / DEG },
  hypot: { min: 1, fn: (...a) => Math.hypot(...a) },
};
const KEYWORDS = new Set(["and", "or", "not", "true", "false", "pi"]);

export class ExpressionParser {
  constructor(source, params) {
    this.source = source;
    this.params = params;
    this.index = 0;
  }

  parse() {
    if (this.source.length > 2000) fail("expression is longer than 2000 characters");
    const result = this.ternary();
    this.space();
    if (this.index !== this.source.length) {
      fail(`unexpected token in expression ${JSON.stringify(this.source)} at ${this.index}`);
    }
    return finite(result, `expression ${JSON.stringify(this.source)}`);
  }

  ternary() {
    const condition = this.or();
    this.space();
    if (!this.take("?")) return condition;
    const yes = this.ternary();
    this.space();
    if (!this.take(":")) fail(`missing ':' in expression ${JSON.stringify(this.source)}`);
    const no = this.ternary();
    return condition !== 0 ? yes : no;
  }

  or() {
    let value = this.and();
    for (;;) {
      this.space();
      if (this.take("||") || this.word("or")) { const right = this.and(); value = (value !== 0 || right !== 0) ? 1 : 0; }
      else return value;
    }
  }

  and() {
    let value = this.not();
    for (;;) {
      this.space();
      if (this.take("&&") || this.word("and")) { const right = this.not(); value = (value !== 0 && right !== 0) ? 1 : 0; }
      else return value;
    }
  }

  not() {
    this.space();
    if (this.word("not") || (this.source[this.index] === "!" && this.source[this.index + 1] !== "=" && this.take("!"))) return this.not() === 0 ? 1 : 0;
    return this.comparison();
  }

  comparison() {
    const left = this.expression();
    this.space();
    for (const op of ["<=", ">=", "==", "!=", "<", ">"]) {
      if (this.take(op)) {
        const right = this.expression();
        const eps = 1e-9;
        switch (op) {
          case "<=": return left <= right + eps ? 1 : 0;
          case ">=": return left >= right - eps ? 1 : 0;
          case "==": return Math.abs(left - right) <= eps ? 1 : 0;
          case "!=": return Math.abs(left - right) > eps ? 1 : 0;
          case "<": return left < right - eps ? 1 : 0;
          case ">": return left > right + eps ? 1 : 0;
        }
      }
    }
    return left;
  }

  expression() {
    let value = this.term();
    for (;;) {
      this.space();
      if (this.take("+")) value += this.term();
      else if (this.take("-")) value -= this.term();
      else return value;
    }
  }

  term() {
    let value = this.unary();
    for (;;) {
      this.space();
      if (this.take("*")) value *= this.unary();
      else if (this.take("/")) {
        const divisor = this.unary();
        if (divisor === 0) fail(`division by zero in expression ${JSON.stringify(this.source)}`);
        value /= divisor;
      } else if (this.take("%")) {
        const divisor = this.unary();
        if (divisor === 0) fail(`modulo by zero in expression ${JSON.stringify(this.source)}`);
        value = value - divisor * Math.floor(value / divisor);
      } else return value;
    }
  }

  unary() {
    this.space();
    if (this.take("+")) return this.unary();
    if (this.take("-")) return -this.unary();
    return this.primary();
  }

  primary() {
    this.space();
    if (this.take("(")) {
      const value = this.ternary();
      this.space();
      if (!this.take(")")) fail(`missing ')' in expression ${JSON.stringify(this.source)}`);
      return value;
    }
    const number = this.match(/^(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?/);
    if (number) return Number(number);
    const identifier = this.match(/^[A-Za-z_][A-Za-z0-9_]*/);
    if (identifier) {
      this.space();
      if (this.source[this.index] === "(") {
        const fn = Object.hasOwn(FUNCTIONS, identifier) ? FUNCTIONS[identifier] : null;
        if (!fn) fail(`unknown function ${identifier} in expression ${JSON.stringify(this.source)}`);
        this.take("(");
        const args = [];
        this.space();
        if (!this.take(")")) {
          for (;;) {
            args.push(this.ternary());
            this.space();
            if (this.take(")")) break;
            if (!this.take(",")) fail(`expected ',' or ')' in call to ${identifier}`);
          }
        }
        if (args.length < fn.min || (fn.max !== undefined && args.length > fn.max)) {
          fail(`${identifier} takes ${fn.max === fn.min ? fn.min : `${fn.min}${fn.max ? `-${fn.max}` : "+"}`} argument(s), got ${args.length}`);
        }
        return fn.fn(...args);
      }
      if (identifier === "true") return 1;
      if (identifier === "false") return 0;
      if (identifier === "pi") return Math.PI;
      if (KEYWORDS.has(identifier)) fail(`unexpected keyword ${identifier} in expression ${JSON.stringify(this.source)}`);
      if (!Object.hasOwn(this.params, identifier)) fail(`unknown parameter ${identifier}`);
      return finite(this.params[identifier], `parameter ${identifier}`);
    }
    fail(`expected number, parameter, function, or '(' in expression ${JSON.stringify(this.source)} at ${this.index}`);
  }

  word(value) {
    if (!this.source.startsWith(value, this.index)) return false;
    const next = this.source[this.index + value.length] || "";
    if (/[A-Za-z0-9_]/.test(next)) return false;
    this.index += value.length;
    return true;
  }

  match(pattern) {
    const found = this.source.slice(this.index).match(pattern);
    if (!found) return "";
    this.index += found[0].length;
    return found[0];
  }

  take(value) {
    if (!this.source.startsWith(value, this.index)) return false;
    this.index += value.length;
    return true;
  }

  space() {
    while (/\s/.test(this.source[this.index] || "")) this.index += 1;
  }
}

export function evaluate(source, params = {}) {
  if (typeof source === "number") return finite(source, "value");
  if (typeof source === "boolean") return source ? 1 : 0;
  return new ExpressionParser(String(source), params).parse();
}
