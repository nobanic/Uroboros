import { browserSession } from "@/astra/client";

export const AUTH_DISABLED = process.env.NEXT_PUBLIC_AUTH_DISABLED === "1";

export interface CurrentUser {
  id: string;
  name: string;
  username: string;
  email: string;
  avatar: string;
  role: string;
}

export const auth = {
  getToken(): string | null {
    if (typeof window === "undefined") return null;
    // Mock demo: no real login; return a fake token so the route guard lets you through straight to the main UI.
    return browserSession.read() ?? (process.env.NEXT_PUBLIC_MOCK === "1" ? "mock-demo" : null);
  },

  setToken(token: string): void {
    browserSession.save(token);
  },

  clearToken(): void {
    browserSession.clear();
  },

  // parse the current user from the JWT payload's sub field; display only, no signature verification.
  getCurrentUser(): CurrentUser | null {
    const token = this.getToken();
    if (!token) return null;
    try {
      const parts = token.split(".");
      if (parts.length !== 3) return null;
      // base64url → base64
      const payload = JSON.parse(atob(parts[1].replace(/-/g, "+").replace(/_/g, "/")));
      const username: string = payload.sub ?? "Uroboros";
      return { id: "1", name: username, username, email: "", avatar: "", role: "operator" };
    } catch {
      return null;
    }
  },
};
