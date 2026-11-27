export { a } from "./a.test";
export * from "./b.js";
export * as ns from "./c";
export { x as y } from "pkg";
export type { T } from "./types";

export function facade() {
    return a();
}
