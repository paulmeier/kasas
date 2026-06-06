function OnTransactionCreate(txn)
  -- A tight pure-Lua loop: the per-hook timeout (ctx attached to the VM) must
  -- interrupt this promptly.
  while true do end
end
