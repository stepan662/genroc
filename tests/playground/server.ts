// Task handler implementations for the order-pipeline playground.
// HTTP plumbing, routing, and AJV validation live in generated/server.ts.
//
// Usage: bun run playground:server

import { sleep } from "bun";
import { startServer, type Handlers } from "./generated/server.ts";

const PORT = 3001;

const handlers: Handlers = {
  async first(input) {
    await sleep(input.sleep);
    return { slept: input.sleep };
  },
  async second(input) {
    await sleep(input.sleep);
    return { slept: input.sleep };
  },
  async third(input) {
    await sleep(input.sleep);
    return { slept: input.sleep };
  },
  async fourth(input) {
    await sleep(input.sleep);
    return { slept: input.sleep };
  },
};

startServer(handlers, PORT);
