<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class ProductTagTranslation extends Model
{
    protected $table = 'product_tag_translations';

    public function productTag(): BelongsTo
    {
        return $this->belongsTo(ProductTag::class);
    }
}
