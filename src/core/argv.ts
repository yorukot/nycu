export function pushPos(argv: string[], value: unknown): void {
  if (value === undefined) return;
  argv.push(String(value));
}

export function pushFlag(argv: string[], flag: string, value: unknown): void {
  if (value === undefined) return;

  if (typeof value === "boolean") {
    if (value) argv.push(flag);
    return;
  }

  if (Array.isArray(value)) {
    for (const item of value) {
      argv.push(flag, String(item));
    }
    return;
  }

  argv.push(flag, String(value));
}

export function pushBoolFlag(argv: string[], flag: string, value: boolean | undefined, falseFlag?: string): void {
  if (value === true) {
    argv.push(flag);
  } else if (value === false && falseFlag) {
    argv.push(falseFlag);
  }
}
