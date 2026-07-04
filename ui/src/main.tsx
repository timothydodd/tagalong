import React from "react";
import ReactDOM from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import "./styles.css";
import App from "./App";
import AppsList from "./pages/AppsList";
import AppForm from "./pages/AppForm";
import AppDetail from "./pages/AppDetail";
import Activity from "./pages/Activity";
import Settings from "./pages/Settings";

const router = createBrowserRouter([
  {
    path: "/",
    element: <App />,
    children: [
      { index: true, element: <AppsList /> },
      { path: "apps/new", element: <AppForm /> },
      { path: "apps/:id", element: <AppDetail /> },
      { path: "apps/:id/edit", element: <AppForm /> },
      { path: "activity", element: <Activity /> },
      { path: "settings", element: <Settings /> },
    ],
  },
]);

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>
);
