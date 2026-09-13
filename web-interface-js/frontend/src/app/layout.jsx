export const metadata = {
  title: "CASUYA-LIVE — Operational Dashboard",
  description:
    "Real-time visibility into study-phase matches, live bets, and system diagnostics for the CASUYA-LIVE trading platform.",
};

export default function RootLayout({ children }) {
  return (
    <html lang="en">
      <body
        style={{
          margin: 0,
          backgroundColor: "#0b0f17",
          color: "#e6edf3",
          fontFamily:
            "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
        }}
      >
        {children}
      </body>
    </html>
  );
}