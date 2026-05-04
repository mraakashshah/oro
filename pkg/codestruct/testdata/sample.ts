export function publicFunc(x: number): string {
    return String(x);
}

function unexportedFunc(): void {}

export class MyClass {
    publicMethod(): string {
        return "hello";
    }

    private privateMethod(): void {}
}

export interface MyInterface {
    doSomething(): void;
}

export type MyType = string | number;

export const arrowFunc = (x: number): string => String(x);
