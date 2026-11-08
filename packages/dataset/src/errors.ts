export class DataSetError extends Error {
  readonly code: string;
  readonly status: number;
  constructor(message: string, code: string, status = 0) {
    super(message);
    this.name = 'DataSetError';
    this.code = code;
    this.status = status;
  }
}
