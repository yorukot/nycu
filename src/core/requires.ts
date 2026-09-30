import { platform } from "node:os";
import { augmentedEnv } from "./exec.ts";

export interface EvaluateOptions {
  check?: (requirement: string) => Promise<boolean>;
  timeoutMs?: number;
}
const cache = new Map<string, Promise<boolean>>();
export function clearRequirementCache(): void { cache.clear(); }

async function checkRequirement(requirement: string): Promise<boolean> {
  const [kind, value] = requirement.split(":", 2);
  if (!value) return false;
  if (kind === "platform") {
    const forced = process.env.NYCU_FORCE_PLATFORM ?? process.env.UTILS_FORCE_PLATFORM;
    return (forced?.trim() || platform()) === value;
  }
  if (kind === "binary") return Bun.which(value, { PATH: augmentedEnv().PATH }) !== null;
  return false;
}

export async function evaluateRequirements(requirements: string[], options: EvaluateOptions = {}) {
  const results = await Promise.all(requirements.map(requirement => {
    let pending = cache.get(requirement);
    if (!pending) {
      pending = (async () => {
        let timer: ReturnType<typeof setTimeout> | undefined;
        try {
          return await Promise.race([
            Promise.resolve().then(() => (options.check ?? checkRequirement)(requirement)),
            new Promise<boolean>(resolve => { timer = setTimeout(() => resolve(false), options.timeoutMs ?? 4000); }),
          ]);
        } catch { return false; }
        finally { clearTimeout(timer); }
      })();
      cache.set(requirement, pending);
    }
    return pending;
  }));
  const failed = requirements.filter((_, index) => !results[index]);
  return { ok: failed.length === 0, failed };
}
