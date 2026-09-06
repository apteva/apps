import { GlobalRegistrator } from "@happy-dom/global-registrator";
GlobalRegistrator.register({ url: "http://localhost" });
(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
