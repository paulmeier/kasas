function OnTransactionCreate(txn) {
  // A tight loop: the per-hook timeout (the watcher goroutine calls vm.Interrupt)
  // must stop this promptly rather than letting it run forever.
  while (true) {}
}
