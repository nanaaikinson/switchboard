import { Link } from "@tanstack/react-router";

export function NotFound() {
  return (
    <div className="py-16 text-center">
      <h1 className="text-lg font-semibold">Page not found</h1>
      <Link to="/" className="mt-2 inline-block text-sm underline underline-offset-4">
        Back to routes
      </Link>
    </div>
  );
}
