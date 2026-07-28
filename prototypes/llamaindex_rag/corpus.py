from __future__ import annotations

from pathlib import Path

from llama_index.core import Document


def load_documents(data_path: Path, chunk_size: int, chunk_overlap: int) -> list[Document]:
    try:
        from .chunking import build_chunk_records
    except ImportError:
        from chunking import build_chunk_records

    documents: list[Document] = []
    for chunk in build_chunk_records(data_path, chunk_size=chunk_size, chunk_overlap=chunk_overlap):
        documents.append(
            Document(
                text=chunk.text,
                metadata=chunk.metadata,
            )
        )
    return documents
