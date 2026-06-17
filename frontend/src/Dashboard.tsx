import { useState, useEffect, useRef, useCallback } from "react";

const API_BASE = "";
const WALLET_SVC = "";
const LEDGER_SVC = "";

const COLORS = {
  bg: "#111111",
  surface: "#1a1a1a",
  surface2: "#222222",
  border: "#2a2a2a",
  text: "#e8e8e8",
  muted: "#555555",
  primary: "#ffffff",
  success: "#4ade80",
  danger: "#f87171",
  warning: "#fbbf24",
};

const styles = {
  root: {
    fontFamily: "'DM Mono', 'Fira Code', 'JetBrains Mono', monospace",
    background: COLORS.bg,
    color: COLORS.text,
    minHeight: "100vh",
    padding: "2rem",
    lineHeight: 1.6,
    letterSpacing: "-0.01em",
  },
  container: { maxWidth: 1100, margin: "0 auto" },
  header: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    paddingBottom: "1.5rem",
    marginBottom: "1.5rem",
    borderBottom: `1px solid ${COLORS.border}`,
  },
  logo: { display: "flex", alignItems: "center", gap: "0.75rem" },
  logoMark: {
    width: 28,
    height: 28,
    background: COLORS.text,
    borderRadius: 4,
    display: "grid",
    placeItems: "center",
    color: COLORS.bg,
    fontSize: "0.65rem",
    fontWeight: 700,
    letterSpacing: "0.05em",
  },
  logoText: {
    fontSize: "0.95rem",
    fontWeight: 500,
    color: COLORS.text,
    letterSpacing: "0.02em",
  },
  statusPill: {
    display: "flex",
    gap: "1.5rem",
    alignItems: "center",
    background: COLORS.surface,
    border: `1px solid ${COLORS.border}`,
    borderRadius: 8,
    padding: "0.6rem 1rem",
    fontSize: "0.8rem",
  },
  card: {
    background: COLORS.surface,
    border: `1px solid ${COLORS.border}`,
    borderRadius: 8,
    padding: "1.25rem",
    marginBottom: "1rem",
  },
  cardTitle: {
    fontSize: "0.75rem",
    fontWeight: 600,
    color: COLORS.muted,
    textTransform: "uppercase",
    letterSpacing: "0.12em",
    marginBottom: "1rem",
  },
  grid: {
    display: "grid",
    gridTemplateColumns: "1fr 1fr",
    gap: "1rem",
    marginBottom: "1rem",
  },
  label: {
    display: "block",
    fontSize: "0.7rem",
    color: COLORS.muted,
    textTransform: "uppercase",
    letterSpacing: "0.1em",
    marginBottom: "0.3rem",
    marginTop: "0.85rem",
  },
  input: {
    width: "100%",
    background: COLORS.bg,
    border: `1px solid ${COLORS.border}`,
    borderRadius: 6,
    padding: "0.5rem 0.75rem",
    color: COLORS.text,
    fontFamily: "inherit",
    fontSize: "0.85rem",
    outline: "none",
    boxSizing: "border-box",
    transition: "border-color 0.15s",
  },
  row: {
    display: "flex",
    gap: "0.5rem",
    marginTop: "0.85rem",
    alignItems: "flex-end",
  },
  btn: (variant = "default") => ({
    padding: "0.5rem 1rem",
    borderRadius: 6,
    border: `1px solid ${
      variant === "ghost"
        ? COLORS.border
        : variant === "success"
          ? COLORS.success
          : variant === "danger"
            ? COLORS.danger
            : COLORS.text
    }`,
    background:
      variant === "ghost"
        ? "transparent"
        : variant === "success"
          ? "rgba(74,222,128,0.08)"
          : variant === "danger"
            ? "rgba(248,113,113,0.08)"
            : COLORS.text,
    color:
      variant === "ghost"
        ? COLORS.muted
        : variant === "success"
          ? COLORS.success
          : variant === "danger"
            ? COLORS.danger
            : COLORS.bg,
    fontFamily: "inherit",
    fontSize: "0.8rem",
    fontWeight: 500,
    cursor: "pointer",
    whiteSpace: "nowrap",
    letterSpacing: "0.02em",
    transition: "opacity 0.15s",
  }),
  scenarioGrid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fill, minmax(160px, 1fr))",
    gap: "0.5rem",
  },
  scenarioBtn: {
    background: COLORS.surface2,
    border: `1px solid ${COLORS.border}`,
    borderRadius: 6,
    padding: "0.65rem 0.85rem",
    color: COLORS.muted,
    fontFamily: "inherit",
    fontSize: "0.78rem",
    cursor: "pointer",
    textAlign: "left",
    transition: "color 0.15s, border-color 0.15s",
    letterSpacing: "0.01em",
  },
  logPanel: {
    background: COLORS.surface,
    border: `1px solid ${COLORS.border}`,
    borderRadius: 8,
    overflow: "hidden",
  },
  logHeader: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    padding: "0.65rem 1rem",
    borderBottom: `1px solid ${COLORS.border}`,
    fontSize: "0.7rem",
    color: COLORS.muted,
    textTransform: "uppercase",
    letterSpacing: "0.1em",
  },
  logBody: {
    height: 280,
    overflowY: "auto",
    padding: "0.5rem 1rem",
    background: COLORS.bg,
    fontFamily: "inherit",
    fontSize: "0.78rem",
  },
  logEntry: {
    display: "flex",
    gap: "0.6rem",
    alignItems: "baseline",
    padding: "0.3rem 0",
    borderBottom: `1px solid ${COLORS.border}`,
  },
  logTime: {
    color: COLORS.muted,
    minWidth: 65,
    flexShrink: 0,
    fontSize: "0.72rem",
  },
  logBadge: (level) => ({
    fontSize: "0.65rem",
    fontWeight: 700,
    textTransform: "uppercase",
    letterSpacing: "0.08em",
    color:
      level === "success"
        ? COLORS.success
        : level === "error"
          ? COLORS.danger
          : level === "warning"
            ? COLORS.warning
            : COLORS.muted,
    minWidth: 42,
    flexShrink: 0,
  }),
  dot: (healthy) => ({
    width: 6,
    height: 6,
    borderRadius: "50%",
    background: healthy ? COLORS.success : COLORS.danger,
    display: "inline-block",
    marginRight: 6,
  }),
  // New styles for idempotency table and advanced grid
  idemTable: {
    maxHeight: 200,
    overflowY: "auto",
    marginTop: "0.5rem",
    fontSize: "0.78rem",
  },
  idemRow: {
    padding: "0.4rem",
    borderBottom: `1px solid ${COLORS.border}`,
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
  },
  advancedSubGrid: {
    display: "grid",
    gridTemplateColumns: "1fr 1fr 1fr",
    gap: "1rem",
  },
  advancedSubCard: {
    background: COLORS.surface2,
    borderRadius: 6,
    padding: "0.85rem",
  },
  subTitle: {
    fontSize: "0.8rem",
    fontWeight: 500,
    marginBottom: "0.75rem",
    color: COLORS.text,
  },
};

function StatusDot({ healthy }) {
  return <span style={styles.dot(healthy)} />;
}

function Btn({ children, onClick, variant = "default", style: extra }) {
  return (
    <button onClick={onClick} style={{ ...styles.btn(variant), ...extra }}>
      {children}
    </button>
  );
}

function Input({
  id,
  placeholder,
  value,
  onChange,
  type = "text",
  style: extra,
}) {
  return (
    <input
      id={id}
      type={type}
      placeholder={placeholder}
      value={value}
      onChange={onChange}
      style={{ ...styles.input, ...extra }}
      onFocus={(e) => (e.target.style.borderColor = COLORS.text)}
      onBlur={(e) => (e.target.style.borderColor = COLORS.border)}
    />
  );
}

export default function Dashboard() {
  const [walletStatus, setWalletStatus] = useState("checking");
  const [ledgerStatus, setLedgerStatus] = useState("checking");
  const [logs, setLogs] = useState([]);
  const [createdWallet, setCreatedWallet] = useState(null);
  const [memo, setMemo] = useState("");

  const [currency, setCurrency] = useState("USD");
  const [walletId, setWalletId] = useState("");
  const [amount, setAmount] = useState("1000");
  const [srcId, setSrcId] = useState("");
  const [destId, setDestId] = useState("");
  const [transferAmount, setTransferAmount] = useState("500");
  const [idemKey, setIdemKey] = useState("");

  // Time-Travel Debugger state
  const [ttWalletId, setTtWalletId] = useState("");
  const [ttTimestamp, setTtTimestamp] = useState("");
  const [ttResult, setTtResult] = useState("");

  // Idempotency Explorer state
  const [idemKeys, setIdemKeys] = useState([]);

  // Chaos Mode state
  const [chaosEnabled, setChaosEnabled] = useState(false);

  // Advanced Features state
  const [fxSrc, setFxSrc] = useState("");
  const [fxDest, setFxDest] = useState("");
  const [fxCurrency, setFxCurrency] = useState("INR");
  const [fxAmount, setFxAmount] = useState("");
  const [disputeRef, setDisputeRef] = useState("");
  const [proofWalletId, setProofWalletId] = useState("");

  const logBodyRef = useRef(null);

  const addLog = useCallback((msg, level = "info", data = null) => {
    const entry = {
      id: Date.now() + Math.random(),
      time: new Date().toLocaleTimeString([], {
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
      }),
      msg,
      level,
      data: data ? JSON.stringify(data).slice(0, 200) : null,
    };
    setLogs((prev) => [...prev.slice(-199), entry]);
  }, []);

  const clearLog = () => setLogs([]);

  useEffect(() => {
    if (logBodyRef.current)
      logBodyRef.current.scrollTop = logBodyRef.current.scrollHeight;
  }, [logs]);

  const apiCall = useCallback(
    async (url, method, body = null, idKey = "") => {
      const headers = { "Content-Type": "application/json" };
      if (idKey) headers["Idempotency-Key"] = idKey;
      const opts = { method, headers };
      if (body && ["POST", "PUT", "PATCH"].includes(method))
        opts.body = JSON.stringify(body);
      const start = performance.now();
      try {
        const resp = await fetch(url, opts);
        const duration = ((performance.now() - start) / 1000).toFixed(2);
        const data = await resp.json().catch(() => null);
        addLog(
          `${method} ${url.replace(API_BASE, "") || "/"}`,
          resp.ok ? "success" : "error",
          {
            status: resp.status,
            duration: `${duration}s`,
            ...(data || {}),
          },
        );
        return { ok: resp.ok, status: resp.status, data, duration };
      } catch (e) {
        addLog(`${method} ${url} — Network Error`, "error", {
          error: e.message,
        });
        return { ok: false, error: e.message };
      }
    },
    [addLog],
  );

  const checkHealth = useCallback(async () => {
    try {
      const [w, l] = await Promise.all([
        fetch(`${WALLET_SVC}/health`)
          .then((r) => r.json())
          .catch(() => ({ status: "down" })),
        fetch(`${LEDGER_SVC}/health`)
          .then((r) => r.json())
          .catch(() => ({ status: "down" })),
      ]);
      setWalletStatus(w.status);
      setLedgerStatus(l.status);
      addLog("Health check", "info", { wallet: w.status, ledger: l.status });
    } catch (e) {
      addLog("Health check failed", "error", { error: e.message });
    }
  }, [addLog]);

  useEffect(() => {
    checkHealth();
    const t = setInterval(checkHealth, 30000);
    return () => clearInterval(t);
  }, [checkHealth]);

  // --- Time-Travel ---
  const fetchHistoricalBalance = useCallback(async () => {
    if (!ttWalletId || !ttTimestamp) {
      addLog("Missing wallet ID or timestamp", "error");
      setTtResult("❌ Missing wallet ID or timestamp");
      return;
    }
    const dt = `${ttTimestamp}Z`;
    const url = `${LEDGER_SVC}/wallets/${ttWalletId}/balance-at?at=${dt}`;
    const res = await apiCall(url, "GET");
    if (res.ok && res.data?.balance_at !== undefined) {
      setTtResult(`💰 Balance: ${res.data.balance_at}`);
    } else {
      setTtResult("❌ Failed to fetch balance");
    }
  }, [ttWalletId, ttTimestamp, apiCall]);

  // --- Idempotency Explorer ---
  const loadIdemKeys = useCallback(async () => {
    const res = await apiCall(`${LEDGER_SVC}/idempotency/keys?limit=20`, "GET");
    if (res.ok && res.data) {
      setIdemKeys(res.data);
      addLog(`Loaded ${res.data.length} idempotency keys`, "success");
    } else {
      addLog("Failed to load idempotency keys", "error");
    }
  }, [apiCall, addLog]);

  const toggleChaos = useCallback(async () => {
    const newState = !chaosEnabled;

    const res = await apiCall(
      `${LEDGER_SVC}/admin/chaos`,
      "POST",
      { enabled: newState },
      `chaos-${Date.now()}`,
    );

    if (res.ok) {
      setChaosEnabled(newState);

    addLog(
      `Chaos mode ${newState ? "ENABLED" : "DISABLED"}`,
      newState ? "warning" : "info",
    );  
  } else {
      addLog("Failed to toggle chaos mode", "error");
    }
  }, [chaosEnabled, apiCall, addLog]);

  // --- Advanced Features ---
  const runFXTransfer = useCallback(async () => {
    if (!fxSrc || !fxDest || !fxAmount) {
      addLog("Missing FX transfer details", "error");
      return;
    }
    const amt = parseInt(fxAmount);
    if (isNaN(amt)) {
      addLog("Invalid amount", "error");
      return;
    }
    const idempotencyKey = crypto.randomUUID();

    const res = await apiCall(
      `${LEDGER_SVC}/wallets/${fxSrc}/transfer/fx`,
      "POST",
      {
        destination_id: fxDest,
        target_currency: fxCurrency,
        amount: amt,
        memo: "fx-conversion",
      },
      idempotencyKey
    );
    if (res.ok) addLog("FX transfer completed", "success");
  }, [fxSrc, fxDest, fxCurrency, fxAmount, apiCall, addLog]);

  const runDispute = useCallback(async () => {
    if (!disputeRef) {
      addLog("Missing transaction reference ID", "error");
      return;
    }
    // Using the global srcId from wallet service as the wallet_id for dispute
    if (!srcId) {
      addLog(
        "Please provide a source wallet ID (from Wallet Service section)",
        "error",
      );
      return;
    }
    const res = await apiCall(`${LEDGER_SVC}/admin/dispute`, "POST", {
      ref_id: disputeRef,
      wallet_id: srcId,
      reason: "customer_request",
    });
    if (res.ok) addLog(`Dispute registered for ${disputeRef}`, "warning");
  }, [disputeRef, srcId, apiCall, addLog]);

  const runProof = useCallback(async () => {
    if (!proofWalletId) {
      addLog("Missing wallet ID for proof", "error");
      return;
    }
    const nonce = Date.now().toString();
    const res = await apiCall(
      `${LEDGER_SVC}/wallets/${proofWalletId}/proof?nonce=${nonce}`,
      "GET",
    );
    if (res.ok && res.data?.balance_commitment) {
      addLog(
        `Proof Generated: ${res.data.balance_commitment.substring(0, 16)}...`,
        "success",
      );
    } else {
      addLog("Failed to generate proof", "error");
    }
  }, [proofWalletId, apiCall, addLog]);

  const loadWebhooks = useCallback(async () => {
    const res = await apiCall(`${LEDGER_SVC}/webhooks/deliveries`, "GET");
    if (res.ok && res.data) {
      addLog(`Webhooks deliveries: ${JSON.stringify(res.data)}`, "info");
    }
  }, [apiCall, addLog]);

  const downloadPostmortem = useCallback(async () => {
    try {
      const resp = await fetch(`${LEDGER_SVC}/admin/postmortem`);
      if (!resp.ok) throw new Error("Failed to fetch postmortem");
      const text = await resp.text();
      const blob = new Blob([text], { type: "text/markdown" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "postmortem.md";
      a.click();
      URL.revokeObjectURL(url);
      addLog("Post-mortem downloaded", "success");
    } catch (e) {
      addLog(`Post-mortem download failed: ${e.message}`, "error");
    }
  }, [addLog]);

  const createWallet = async () => {
    if (!/^[A-Z]{3}$/i.test(currency)) {
      addLog("Invalid currency code", "error");
      return;
    }
    const res = await apiCall(`${WALLET_SVC}/wallets`, "POST", {
      currency: currency.toUpperCase(),
    });
    if (res.ok && res.data?.id) {
      setCreatedWallet(res.data.id);
      setWalletId(res.data.id);
      setSrcId(res.data.id);
    }
  };

  const deposit = async () => {
  if (!walletId || !amount) {
    addLog("Missing wallet ID or amount", "error");
    return;
  }
  const idempotencyKey = `deposit-${Date.now()}-${Math.random()}`;
  const BANK_WALLET_ID = `${import.meta.env.VITE_BANKDB}`;
  const res = await apiCall(
      `${LEDGER_SVC}/wallets/${BANK_WALLET_ID}/transfer`,
      "POST",
      {
        destination_id: walletId,
        amount: parseInt(amount),
        reference_id: `deposit-${Date.now()}`,
        memo: "Customer deposit",
      },
      idempotencyKey
    );
    if (!res.ok) {
      addLog(`Deposit failed: ${res.data?.error || "unknown error"}`, "error");
    }
  };

  const withdraw = async () => {
    if (!walletId || !amount) {
      addLog("Missing wallet ID or amount", "error");
      return;
    }
    await apiCall(`${WALLET_SVC}/wallets/${walletId}/withdraw`, "POST", {
      amount: parseInt(amount),
      reference_id: `web-${Date.now()}`,
    });
  };

  const transfer = async () => {
    if (!srcId || !destId || !transferAmount) {
      addLog("Missing transfer details", "error");
      return;
    }
    await apiCall(
      `${LEDGER_SVC}/wallets/${srcId}/transfer`,
      "POST",
      {
        destination_id: destId,
        amount: parseInt(transferAmount),
        reference_id: `web-${Date.now()}`,
        memo: memo,
      },
      idemKey || `web-${Date.now()}`,
    );
  };

  const reconcile = async () => {
    const res = await apiCall(`${LEDGER_SVC}/reconcile`, "GET");
    if (res.ok && res.data) {
      const { status, discrepancy_paise } = res.data;
      addLog(
        `Reconcile: ${status?.toUpperCase()} ${discrepancy_paise !== 0 ? `Diff: ${discrepancy_paise}` : ""}`,
        discrepancy_paise === 0 ? "success" : "warning",
        res.data,
      );
    }
  };

  const runScenario = async (name) => {
    clearLog();
    addLog(`Starting scenario: ${name}`, "info");

    if (name === "concurrent") {
      const [w1, w2] = await Promise.all([
        apiCall(`${WALLET_SVC}/wallets`, "POST", { currency: "USD" }),
        apiCall(`${WALLET_SVC}/wallets`, "POST", { currency: "USD" }),
      ]);
      const src = w1.data?.id,
        dest = w2.data?.id;
      if (!src || !dest) {
        addLog("Failed to create wallets", "error");
        return;
      }
      await Promise.all([
        apiCall(`${WALLET_SVC}/wallets/${src}/deposit`, "POST", {
          amount: 10000,
          reference_id: "seed",
        }),
        apiCall(`${WALLET_SVC}/wallets/${dest}/deposit`, "POST", {
          amount: 10000,
          reference_id: "seed",
        }),
      ]);
      const promises = [];
      for (let i = 0; i < 20; i++) {
        promises.push(
          apiCall(
            `${LEDGER_SVC}/wallets/${src}/transfer`,
            "POST",
            {
              destination_id: dest,
              amount: 10,
              reference_id: `c-${i}`,
              memo: `${Date.now()} - memo`,
            },
            `key-XY-${i}`,
          ),
        );
        promises.push(
          apiCall(
            `${LEDGER_SVC}/wallets/${dest}/transfer`,
            "POST",
            {
              destination_id: src,
              amount: 10,
              reference_id: `c-${i}`,
              memo: `${Date.now()} - memo`,
            },
            `key-YX-${i}`,
          ),
        );
      }
      await Promise.allSettled(promises);
      await reconcile();
    } else if (name === "errors") {
      await apiCall(`${WALLET_SVC}/wallets`, "POST", { currency: "XX" });
      const w = await apiCall(`${WALLET_SVC}/wallets`, "POST", {
        currency: "USD",
      });
      if (w.data?.id) {
        await apiCall(`${WALLET_SVC}/wallets/${w.data.id}/withdraw`, "POST", {
          amount: 999999,
          reference_id: "err",
        });
        await apiCall(
          `${LEDGER_SVC}/wallets/${w.data.id}/transfer`,
          "POST",
          { destination_id: "nonexistent", amount: 50, memo: `${Date.now()}` },
          "err-key",
        );
      }
    } else if (name === "idempotency") {
      const [a, b] = await Promise.all([
        apiCall(`${WALLET_SVC}/wallets`, "POST", { currency: "USD" }),
        apiCall(`${WALLET_SVC}/wallets`, "POST", { currency: "USD" }),
      ]);
      if (!a.data?.id || !b.data?.id) return;
      await apiCall(`${WALLET_SVC}/wallets/${a.data.id}/deposit`, "POST", {
        amount: 500,
        reference_id: "seed",
      });
      const key = `idem-${Date.now()}`;
      await apiCall(
        `${LEDGER_SVC}/wallets/${a.data.id}/transfer`,
        "POST",
        {
          destination_id: b.data.id,
          amount: 100,
          reference_id: "idem",
          memo: `${Date.now()}`,
        },
        key,
      );
      await apiCall(
        `${LEDGER_SVC}/wallets/${a.data.id}/transfer`,
        "POST",
        {
          destination_id: b.data.id,
          amount: 9999,
          reference_id: "HACK",
          memo: `${Date.now()}`,
        },
        key,
      );
      addLog(
        "Second call should return cached response — balance unchanged",
        "info",
      );
    } else if (name === "full") {
      const [f1, f2] = await Promise.all([
        apiCall(`${WALLET_SVC}/wallets`, "POST", { currency: "USD" }),
        apiCall(`${WALLET_SVC}/wallets`, "POST", { currency: "USD" }),
      ]);
      if (!f1.data?.id || !f2.data?.id) return;
      await apiCall(`${WALLET_SVC}/wallets/${f1.data.id}/deposit`, "POST", {
        amount: 5000,
        reference_id: "life-1",
      });
      await apiCall(`${WALLET_SVC}/wallets/${f1.data.id}/withdraw`, "POST", {
        amount: 2000,
        reference_id: "life-2",
      });
      await apiCall(
        `${LEDGER_SVC}/wallets/${f1.data.id}/transfer`,
        "POST",
        { destination_id: f2.data.id, amount: 1000, reference_id: "life-3" },
        "life-full",
      );
      await reconcile();
    } else if (name === "stress") {
      const w = await apiCall(`${WALLET_SVC}/wallets`, "POST", {
        currency: "USD",
      });
      if (!w.data?.id) return;
      await apiCall(`${WALLET_SVC}/wallets/${w.data.id}/deposit`, "POST", {
        amount: 100000,
        reference_id: "stress",
      });
      const promises = [];
      for (let i = 0; i < 100; i++) {
        promises.push(
          apiCall(
            `${WALLET_SVC}/wallets/${w.data.id}/deposit`,
            "POST",
            { amount: 1, reference_id: `stress-${i}` },
            `stress-key-${i}`,
          ),
        );
      }
      const results = await Promise.allSettled(promises);
      const success = results.filter(
        (r) => r.status === "fulfilled" && r.value.ok,
      ).length;
      addLog(
        `Stress test: ${success}/100 requests succeeded`,
        success === 100 ? "success" : "warning",
      );
      await reconcile();
    }

    addLog(`Scenario "${name}" completed`, "success");
  };

  const resetDemo = () => {
    if (!confirm("Clear demo wallets and logs?")) return;
    clearLog();
    setCreatedWallet(null);
    setWalletId("");
    setSrcId("");
    setDestId("");
    addLog("Demo state reset", "info");
  };

  const scenarios = [
    { id: "concurrent", label: "Concurrent Transfers" },
    { id: "errors", label: "Error Cases" },
    { id: "idempotency", label: "Idempotency Replay" },
    { id: "full", label: "Full Lifecycle" },
    { id: "stress", label: "Stress Test (100)" },
  ];

  return (
    <div style={styles.root}>
      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=DM+Mono:wght@400;500&display=swap');
        * { box-sizing: border-box; margin: 0; padding: 0; }
        ::-webkit-scrollbar { width: 4px; } 
        ::-webkit-scrollbar-track { background: transparent; }
        ::-webkit-scrollbar-thumb { background: #2a2a2a; border-radius: 2px; }
        button:hover { opacity: 0.8; }
        .scenario-btn:hover { color: #e8e8e8 !important; border-color: #444 !important; }
      `}</style>

      <div style={styles.container}>
        {/* Header */}
        <header style={styles.header}>
          <div style={styles.logo}>
            <div style={styles.logoMark}>PL</div>
            <span style={styles.logoText}>PocketLedger</span>
          </div>
          <div style={styles.statusPill}>
            <span>
              <StatusDot healthy={walletStatus === "healthy"} />
              Wallet
              <span
                style={{
                  color:
                    walletStatus === "healthy" ? COLORS.success : COLORS.danger,
                  marginLeft: 4,
                }}
              >
                {walletStatus}
              </span>
            </span>
            <span style={{ color: COLORS.border }}>|</span>
            <span>
              <StatusDot healthy={ledgerStatus === "healthy"} />
              Ledger
              <span
                style={{
                  color:
                    ledgerStatus === "healthy" ? COLORS.success : COLORS.danger,
                  marginLeft: 4,
                }}
              >
                {ledgerStatus}
              </span>
            </span>
            <button
              onClick={checkHealth}
              style={{
                ...styles.btn("ghost"),
                padding: "0.3rem 0.6rem",
                fontSize: "0.72rem",
              }}
            >
              Refresh
            </button>
          </div>
        </header>

        {/* Wallet & Ledger Operations (existing) */}
        <div style={styles.grid}>
          <div style={styles.card}>
            <div style={styles.cardTitle}>Wallet Service</div>
            <label style={styles.label}>Currency</label>
            <div style={{ display: "flex", gap: "0.5rem" }}>
              <Input
                placeholder="USD"
                value={currency}
                onChange={(e) => setCurrency(e.target.value)}
                style={{ maxWidth: 90 }}
              />
              <Btn onClick={createWallet}>Create</Btn>
            </div>
            {createdWallet && (
              <div
                style={{
                  marginTop: "0.6rem",
                  padding: "0.5rem 0.75rem",
                  background: COLORS.bg,
                  border: `1px solid ${COLORS.border}`,
                  borderRadius: 6,
                  fontSize: "0.78rem",
                  color: COLORS.muted,
                  wordBreak: "break-all",
                }}
              >
                <span style={{ color: COLORS.success, marginRight: 6 }}>
                  Created
                </span>
                {createdWallet}
              </div>
            )}
            <label style={styles.label}>Wallet ID</label>
            <Input
              placeholder="Paste wallet ID"
              value={walletId}
              onChange={(e) => setWalletId(e.target.value)}
            />
            <label style={styles.label}>Amount (paise)</label>
            <div style={styles.row}>
              <Input
                type="number"
                placeholder="1000"
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
              />
              <Btn variant="success" onClick={deposit}>
                Deposit
              </Btn>
              <Btn variant="danger" onClick={withdraw}>
                Withdraw
              </Btn>
            </div>
          </div>

          <div style={styles.card}>
            <div style={styles.cardTitle}>Ledger Service</div>
            <label style={styles.label}>Memo</label>
            <Input
              placeholder="Memo"
              value={memo}
              onChange={(e) => setMemo(e.target.value)}
            />
            <label style={styles.label}>Source Wallet</label>
            <Input
              placeholder="Source wallet ID"
              value={srcId}
              onChange={(e) => setSrcId(e.target.value)}
            />
            <label style={styles.label}>Destination Wallet</label>
            <Input
              placeholder="Destination wallet ID"
              value={destId}
              onChange={(e) => setDestId(e.target.value)}
            />
            <label style={styles.label}>Amount</label>
            <Input
              type="number"
              placeholder="500"
              value={transferAmount}
              onChange={(e) => setTransferAmount(e.target.value)}
            />
            <label style={styles.label}>Idempotency Key</label>
            <Input
              placeholder="Optional — auto-generated if blank"
              value={idemKey}
              onChange={(e) => setIdemKey(e.target.value)}
            />
            <div style={{ ...styles.row, marginTop: "1rem" }}>
              <Btn onClick={transfer}>Transfer</Btn>
              <Btn variant="ghost" onClick={reconcile}>
                Reconcile
              </Btn>
            </div>
          </div>
        </div>

        {/* Scenarios */}
        <div style={styles.card}>
          <div style={styles.cardTitle}>Scenarios</div>
          <div style={styles.scenarioGrid}>
            {scenarios.map((s) => (
              <button
                key={s.id}
                className="scenario-btn"
                style={styles.scenarioBtn}
                onClick={() => runScenario(s.id)}
              >
                {s.label}
              </button>
            ))}
          </div>
          <div style={{ display: "flex", gap: "0.5rem", marginTop: "1rem" }}>
            <Btn variant="ghost" onClick={clearLog}>
              Clear Log
            </Btn>
            <Btn variant="danger" onClick={resetDemo}>
              Reset Demo
            </Btn>
          </div>
        </div>

        {/* Time-Travel Debugger Card */}
        <div style={styles.card}>
          <div style={styles.cardTitle}>Time-Travel Debugger</div>
          <div style={styles.row}>
            <Input
              id="tt-wallet"
              placeholder="Wallet ID"
              value={ttWalletId}
              onChange={(e) => setTtWalletId(e.target.value)}
              style={{ flex: 1 }}
            />
            <input
              type="datetime-local"
              step="1"
              value={ttTimestamp}
              onChange={(e) => setTtTimestamp(e.target.value)}
              style={{ ...styles.input, width: "auto" }}
            />
            <Btn onClick={fetchHistoricalBalance}>Replay</Btn>
          </div>
          <div style={{ marginTop: "0.5rem", fontWeight: 600 }}>{ttResult}</div>
        </div>

        {/* Idempotency Explorer Card */}
        <div style={styles.card}>
          <div style={styles.cardTitle}>Idempotency Explorer</div>
          <Btn onClick={loadIdemKeys} variant="secondary">
            Refresh Keys
          </Btn>
          <div style={styles.idemTable}>
            {idemKeys.length === 0 ? (
              <div style={{ color: COLORS.muted, padding: "0.5rem 0" }}>
                No keys loaded. Click refresh.
              </div>
            ) : (
              idemKeys.map((k, idx) => (
                <div key={idx} style={styles.idemRow}>
                  <span>
                    <strong>{k.key}</strong> → {k.status}
                  </span>
                  <span style={{ color: COLORS.muted, fontSize: "0.7rem" }}>
                    {k.created_at}
                  </span>
                </div>
              ))
            )}
          </div>
        </div>

        {/* Chaos Mode Card */}
        <div style={styles.card}>
          <div style={styles.cardTitle}>Chaos Engineering</div>
          <div style={styles.row}>
            <Btn
              onClick={toggleChaos}
              variant={chaosEnabled ? "danger" : "ghost"}
            >
              {chaosEnabled ? "🔴 Disable Chaos Mode" : "⚫ Enable Chaos Mode"}
            </Btn>
            <span style={{ fontSize: "0.75rem", color: COLORS.muted }}>
              {chaosEnabled
                ? "Chaos is ACTIVE — random failures may occur"
                : "Chaos is OFF"}
            </span>
          </div>
        </div>

        {/* Advanced Features Card */}
        <div style={styles.card}>
          <div style={styles.cardTitle}>Advanced Features</div>
          <div style={styles.advancedSubGrid}>
            {/* Multi-Currency Transfer */}
            <div style={styles.advancedSubCard}>
              <div style={styles.subTitle}>Multi-Currency Transfer</div>
              <div style={{ marginBottom: "0.5rem" }}>
                <Input
                  placeholder="Source Wallet ID"
                  value={fxSrc}
                  onChange={(e) => setFxSrc(e.target.value)}
                  style={{ marginBottom: "0.3rem" }}
                />
                <Input
                  placeholder="Destination Wallet ID"
                  value={fxDest}
                  onChange={(e) => setFxDest(e.target.value)}
                  style={{ marginBottom: "0.3rem" }}
                />
                <select
                  value={fxCurrency}
                  onChange={(e) => setFxCurrency(e.target.value)}
                  style={{
                    ...styles.input,
                    marginBottom: "0.3rem",
                    width: "100%",
                  }}
                >
                  <option value="INR">USD </option>
                  <option value="EUR">EUR</option>
                </select>
                <Input
                  type="number"
                  placeholder="Amount (USD)"
                  value={fxAmount}
                  onChange={(e) => setFxAmount(e.target.value)}
                />
              </div>
              <Btn onClick={runFXTransfer}>Convert & Transfer</Btn>
            </div>

            {/* Dispute & Proof */}
            <div style={styles.advancedSubCard}>
              <div style={styles.subTitle}>Dispute & Proof</div>
              <div style={{ marginBottom: "0.5rem" }}>
                <Input
                  placeholder="Transaction Ref ID"
                  value={disputeRef}
                  onChange={(e) => setDisputeRef(e.target.value)}
                  style={{ marginBottom: "0.3rem" }}
                />
                <Btn
                  variant="danger"
                  onClick={runDispute}
                  style={{ marginBottom: "0.5rem", width: "100%" }}
                >
                  Register Dispute
                </Btn>
                <Input
                  placeholder="Wallet ID for Proof"
                  value={proofWalletId}
                  onChange={(e) => setProofWalletId(e.target.value)}
                  style={{ marginBottom: "0.3rem" }}
                />
                <Btn onClick={runProof}>Generate Proof</Btn>
              </div>
            </div>

            {/* System Reports */}
            <div style={styles.advancedSubCard}>
              <div style={styles.subTitle}>System Reports</div>
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  gap: "0.5rem",
                }}
              >
                <Btn onClick={loadWebhooks} variant="secondary">
                  View Webhooks
                </Btn>
                <Btn onClick={downloadPostmortem} variant="secondary">
                  Download Post‑Mortem
                </Btn>
              </div>
            </div>
          </div>
        </div>

        {/* Log Panel */}
        <div style={styles.logPanel}>
          <div style={styles.logHeader}>
            <span>Request Log</span>
            <span>{logs.length} entries</span>
          </div>
          <div style={styles.logBody} ref={logBodyRef}>
            {logs.length === 0 && (
              <div
                style={{
                  color: COLORS.muted,
                  padding: "1rem 0",
                  fontSize: "0.78rem",
                }}
              >
                No entries yet.
              </div>
            )}
            {logs.map((entry) => (
              <div key={entry.id} style={styles.logEntry}>
                <span style={styles.logTime}>{entry.time}</span>
                <span style={styles.logBadge(entry.level)}>{entry.level}</span>
                <span style={{ flex: 1, wordBreak: "break-all" }}>
                  {entry.msg}
                  {entry.data && (
                    <span
                      style={{
                        display: "block",
                        color: COLORS.muted,
                        fontSize: "0.72rem",
                        marginTop: 2,
                      }}
                    >
                      {entry.data}
                    </span>
                  )}
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
