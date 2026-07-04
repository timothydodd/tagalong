import { NavLink, Outlet } from "react-router-dom";

export default function App() {
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="dot" /> tagalong
        </div>
        <nav className="nav">
          <NavLink to="/" end>
            Apps
          </NavLink>
          <NavLink to="/activity">Activity</NavLink>
          <NavLink to="/settings">Settings</NavLink>
        </nav>
      </aside>
      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}
