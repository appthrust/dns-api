// The package currently exposes TypeScript source to tsc. This shim keeps
// repository type checking focused on the public kest API used by our tests.
declare module "@appthrust/kest" {
  export interface K8sResource {
    apiVersion: string;
    kind: string;
    metadata: {
      name: string;
      namespace?: string;
      labels?: Record<string, string>;
      annotations?: Record<string, string>;
      [key: string]: unknown;
    };
    [key: string]: unknown;
  }

  export interface ActionOptions {
    readonly timeout?: string;
    readonly interval?: string;
    readonly stallTimeout?: string;
  }

  export interface K8sResourceReference<T extends K8sResource = K8sResource> {
    readonly apiVersion: T["apiVersion"];
    readonly kind: T["kind"];
    readonly name: string;
    readonly namespace?: string;
  }

  export interface ResourceTest<T extends K8sResource = K8sResource>
    extends K8sResourceReference<T> {
    readonly test: (this: T) => void | Promise<void>;
  }

  export interface LabelInput<T extends K8sResource = K8sResource>
    extends K8sResourceReference<T> {
    readonly labels: Record<string, string | null>;
    readonly overwrite?: boolean;
  }

  export interface ExecContext {
    readonly $: typeof import("bun").$;
  }

  export interface ExecInput<T = unknown> {
    readonly do: (context: ExecContext) => Promise<T>;
    readonly revert?: (context: ExecContext) => Promise<void>;
  }

  export interface Namespace {
    readonly name: string;
    apply<T extends K8sResource>(
      manifest: T,
      options?: ActionOptions
    ): Promise<void>;
    delete<T extends K8sResource>(
      resource: K8sResourceReference<T>,
      options?: ActionOptions
    ): Promise<void>;
    assert<T extends K8sResource>(
      resource: ResourceTest<T>,
      options?: ActionOptions
    ): Promise<T>;
    assertAbsence<T extends K8sResource>(
      resource: K8sResourceReference<T>,
      options?: ActionOptions
    ): Promise<void>;
  }

  export interface Scenario {
    given(description: string): void;
    when(description: string): void;
    then(description: string): void;
    newNamespace(
      name?: string | { readonly generateName: string },
      options?: ActionOptions
    ): Promise<Namespace>;
    generateName(prefix: string): string;
    label<T extends K8sResource>(
      input: LabelInput<T>,
      options?: ActionOptions
    ): Promise<void>;
    exec<T = unknown>(
      input: ExecInput<T>,
      options?: ActionOptions
    ): Promise<T>;
  }

  export function test(
    label: string,
    callback: (scenario: Scenario) => Promise<void>,
    options?: { readonly timeout?: string }
  ): void;
}
