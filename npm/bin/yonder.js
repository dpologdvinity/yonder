#!/usr/bin/env node
"use strict";
const { spawnSync } = require("child_process");
const { install } = require("../install.js");

install()
  .then((bin) => {
    const r = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
    if (r.error) throw r.error;
    process.exit(r.status === null ? 1 : r.status);
  })
  .catch((err) => {
    console.error(`yonder: ${err.message}`);
    process.exit(1);
  });
