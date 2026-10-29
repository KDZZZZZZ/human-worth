import { api, apiUrl } from "../api.js";
export default function ErrorNote({ error, retry }) {
  return (
    error && (
      <div role="alert" className="state-note">
        {error.message}
        {error.code && <small> · {error.code}</small>}
        {error.status === 401 && (
          <a className="text-link" href={apiUrl("/auth/google")}>
            Sign in with Google again
          </a>
        )}
        {retry && (
          <button className="text-link" onClick={retry}>
            Retry
          </button>
        )}
      </div>
    )
  );
}
