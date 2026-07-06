import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useAuth } from "./auth";

export default function App() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();

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
        <div className="sidebar-foot">
          <span className="who" title={user?.username}>
            {user?.username}
          </span>
          <button className="btn sm" onClick={logout}>
            Log out
          </button>
        </div>
      </aside>
      <main className="main">
        {user?.must_change_password && (
          <div className="warn-box">
            You're still using the default <code>admin</code> password.{" "}
            <a href="/settings" onClick={(e) => { e.preventDefault(); navigate("/settings"); }}>
              Change it in Settings →
            </a>
          </div>
        )}
        <Outlet />
      </main>
    </div>
  );
}
