import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "Uroboros",
  version: packageJson.version,
  copyright: `© ${currentYear}, Uroboros.`,
  meta: {
    title: "Uroboros — Autonomous Penetration Testing Console",
    description: "LLM-driven autonomous penetration testing system console",
  },
};
