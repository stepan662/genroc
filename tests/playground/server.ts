// Task handler implementations for the order-pipeline playground.
// HTTP plumbing, routing, and AJV validation live in generated/server.ts.
//
// Usage: bun run playground:server

import { startServer, type Handlers } from "./generated/server.ts";
import { StartInput, StartOutput } from "./generated/types.ts";

const PORT = 3001;

const handlers: Handlers = {
  start: async function (ctx: StartInput): Promise<StartOutput> {
    return { num: ctx, str: "tst" };
  },
};

startServer(handlers, PORT);
