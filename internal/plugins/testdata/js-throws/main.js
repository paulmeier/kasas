function OnTransactionCreate(txn) {
  throw new Error("intentional failure from the js-throws fixture");
}
