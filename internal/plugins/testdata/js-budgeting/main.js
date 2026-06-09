// A tiny budgeting plugin in JavaScript: when a transaction's description contains
// the configured keyword, tag it and flag it via a schema extension. The host API is
// the camelCase `kasas` global; hooks are plain top-level functions.

function classify(txn) {
  if (txn.description && txn.description.toLowerCase().includes(kasas.config.keyword)) {
    kasas.applyLabels(txn.id, { category: "food" });
    kasas.setExtension(txn.id, "budgeting.flagged", true);
    kasas.log("info", "tagged transaction", { id: txn.id });
  }
}

function OnTransactionCreate(txn) {
  classify(txn);
}

function OnTransactionUpdate(txn) {
  classify(txn);
}
