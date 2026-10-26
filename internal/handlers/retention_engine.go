package handlers

// purgeExecutionEnabled is the master switch of the purge engine. It is a constant on purpose: this release
// ships the engine prepared and OFF, and no configuration, request or environment can turn it on. Enabling
// deletion is a code change that comes with the decisions listed in the retention documentation.
const purgeExecutionEnabled = false
