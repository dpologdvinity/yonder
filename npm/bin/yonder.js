#!/usr/bin/env node
"use strict";
const { spawn } = require("child_process");
const { install } = require("../install.js");

install()
  .then((bin) => {
    const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });
    // Pass termination on to yonder: if only this wrapper were killed, the share would keep running.
    for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
      if (process.platform === "win32" && sig === "SIGINT") {
        // On Windows, Ctrl-C already reaches yonder directly, and child.kill() would force-quit it
        // before it can remove half-finished uploads. Stay alive until it exits by itself.
        process.on(sig, () => {});
      } else {
        process.on(sig, () => child.kill(sig));
      }
    }
    child.on("error", (err) => {
      console.error(`yonder: ${err.message}`);
      process.exit(1);
    });
    child.on("exit", (code, signal) => {
      process.exit(code === null ? (signal ? 128 : 1) : code);
    });
  })
  .catch((err) => {
    console.error(`yonder: ${err.message}`);
    process.exit(1);
  });
