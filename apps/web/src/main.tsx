import React from "react";
import ReactDOM from "react-dom/client";
import "@arco-design/web-react/dist/css/arco.css";
import App from "./App";
import "./styles/app.css";
import { BrowserRouter, Route, Routes } from "react-router-dom";
import HomePage from "./pages/HomePage";
import CoinPage from "./pages/CoinPage";
import AnomaliesPage from "./pages/AnomaliesPage";
import IntradayPage from "./pages/IntradayPage";
import SignalLabPage from "./pages/SignalLabPage";
import BollPumpPage from "./pages/BollPumpPage";
import BollSqueezePage from "./pages/BollSqueezePage";
import EmaPullbackPage from "./pages/EmaPullbackPage";
import PotentialPage from "./pages/PotentialPage";
import { NotificationCenterProvider } from "./stores/notificationCenter";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <NotificationCenterProvider>
      <BrowserRouter>
        <Routes>
          <Route path="/" element={<App />}>
            <Route index element={<HomePage />} />
            <Route path="coin/:symbol" element={<CoinPage />} />
            <Route path="intraday" element={<IntradayPage />} />
            <Route path="intraday/:symbol" element={<IntradayPage />} />
            <Route path="anomalies" element={<AnomaliesPage />} />
            <Route path="signal-lab" element={<SignalLabPage />} />
            <Route path="boll-pump" element={<BollPumpPage />} />
            <Route path="boll-squeeze" element={<BollSqueezePage />} />
            <Route path="ema-pullback" element={<EmaPullbackPage />} />
            <Route path="potential" element={<PotentialPage />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </NotificationCenterProvider>
  </React.StrictMode>
);
