// The same budgeting plugin in TypeScript. The interface, type annotations, and the
// `as` cast below are all TypeScript-only syntax: if esbuild did not strip them at
// load, goja would fail to parse this file. A successful load + invoke proves the
// transpile step works.

interface Transaction {
  id: string;
  description: string;
  amount: string;
}

function classify(txn: Transaction): void {
  const keyword = kasas.config.keyword as string;
  if (txn.description && txn.description.toLowerCase().includes(keyword)) {
    kasas.applyLabels(txn.id, { category: "food" });
    kasas.setExtension(txn.id, "budgeting.flagged", true);
  }
}

function OnTransactionCreate(txn: Transaction): void {
  classify(txn);
}
