export function formatLatencyMs(latencyUs: number) {
  return `${(latencyUs / 1_000).toFixed(3)} ms`
}
