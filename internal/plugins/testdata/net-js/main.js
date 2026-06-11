function OnTransactionCreate(txn) {
  const r = kasas.fetch({
    url: "https://api.example.com/x",
    method: "POST",
    headers: { "X-Test": "1" },
    body: "hi",
    timeoutMs: 500,
  });
  kasas.applyLabels(txn.id, { status: String(r.status) });
}
